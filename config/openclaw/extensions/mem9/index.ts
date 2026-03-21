import type { MemoryBackend } from "./backend.js";
import { ServerBackend } from "./server-backend.js";
import { registerHooks } from "./hooks.js";
import type {
  PluginConfig,
  Memory,
  CreateMemoryInput,
  UpdateMemoryInput,
  SearchInput,
  IngestInput,
  IngestResult,
} from "./types.js";

const DEFAULT_API_URL = "https://api.mem9.ai";
const MAX_SEARCH_RESULT_CONTENT = 2000; // max chars per memory in search results

// ---------------------------------------------------------------------------
// Write debouncer — batches rapid-fire memory_store calls to reduce HTTP
// round-trips during bulk ingest. Buffers writes for DEBOUNCE_MS, then
// fires them all as individual store() calls in a single Promise.all().
// ---------------------------------------------------------------------------
const DEBOUNCE_MS = 2_000;
const MAX_TAGS_PER_MEMORY = 5; // cap tags to reduce storage verbosity

interface PendingWrite {
  input: CreateMemoryInput;
  resolve: (v: unknown) => void;
  reject: (e: unknown) => void;
}

class WriteDebouncer {
  private buffer: PendingWrite[] = [];
  private timer: ReturnType<typeof setTimeout> | null = null;
  private recentContents = new Set<string>(); // dedup within a debounce window

  constructor(private backend: MemoryBackend) {}

  enqueue(input: CreateMemoryInput): Promise<unknown> {
    // --- Duplicate check: skip if identical content was already queued ---
    const normalizedContent = input.content.trim().toLowerCase();
    if (this.recentContents.has(normalizedContent)) {
      return Promise.resolve({ ok: true, deduplicated: true });
    }
    this.recentContents.add(normalizedContent);

    // --- Cap tags to MAX_TAGS_PER_MEMORY ---
    if (input.tags && input.tags.length > MAX_TAGS_PER_MEMORY) {
      input.tags = input.tags.slice(0, MAX_TAGS_PER_MEMORY);
    }

    return new Promise((resolve, reject) => {
      this.buffer.push({ input, resolve, reject });
      if (this.timer) clearTimeout(this.timer);
      this.timer = setTimeout(() => this.flush(), DEBOUNCE_MS);
    });
  }

  private async flush(): Promise<void> {
    const batch = this.buffer.splice(0);
    this.recentContents.clear();
    this.timer = null;
    if (batch.length === 0) return;

    // Fire all writes concurrently to reduce wall-clock time
    const results = await Promise.allSettled(
      batch.map((pw) => this.backend.store(pw.input))
    );
    for (let i = 0; i < batch.length; i++) {
      const r = results[i];
      if (r.status === "fulfilled") {
        batch[i].resolve(r.value);
      } else {
        batch[i].reject(r.reason);
      }
    }
  }
}

// ---------------------------------------------------------------------------
// Search result quality filtering — remove noise and near-duplicates
// ---------------------------------------------------------------------------

const MIN_SEARCH_SCORE = 0.4; // below this threshold, results are likely noise
const DEDUP_SIMILARITY_THRESHOLD = 0.8; // 80% content overlap → keep higher-scored one

/**
 * Jaccard-like character bigram similarity.
 * Fast approximation — avoids full edit-distance on every pair.
 */
function contentSimilarity(a: string, b: string): number {
  if (a === b) return 1;
  const shorter = a.length < b.length ? a : b;
  const longer = a.length < b.length ? b : a;
  if (shorter.length === 0) return 0;
  // Quick length-ratio check: if lengths differ by >5x, they can't be 80% similar
  if (shorter.length / longer.length < 0.2) return 0;

  const bigramsA = new Set<string>();
  for (let i = 0; i < shorter.length - 1; i++) bigramsA.add(shorter.slice(i, i + 2));
  let shared = 0;
  for (let i = 0; i < longer.length - 1; i++) {
    if (bigramsA.has(longer.slice(i, i + 2))) shared++;
  }
  const total = (shorter.length - 1) + (longer.length - 1);
  return total > 0 ? (2 * shared) / total : 0;
}

/**
 * Filter search results: drop low-score noise, deduplicate near-identical content.
 */
function filterSearchResults(memories: Memory[]): Memory[] {
  // 1. Remove low-score results
  const scored = memories.filter(
    (m) => m.score == null || m.score >= MIN_SEARCH_SCORE
  );

  // 2. Deduplicate: for each pair sharing >80% content, keep the higher-scored one
  const kept: Memory[] = [];
  for (const m of scored) {
    let dominated = false;
    for (const existing of kept) {
      const sim = contentSimilarity(
        m.content.toLowerCase(),
        existing.content.toLowerCase(),
      );
      if (sim >= DEDUP_SIMILARITY_THRESHOLD) {
        // Keep the one with the higher score; if same, keep existing (first-in wins)
        if ((m.score ?? 0) > (existing.score ?? 0)) {
          kept.splice(kept.indexOf(existing), 1);
        } else {
          dominated = true;
        }
        break;
      }
    }
    if (!dominated) kept.push(m);
  }

  return kept;
}

function jsonResult(data: unknown) {
  // Older OpenClaw versions may assume tool results have a normalized
  // assistant-content shape and can crash on plain objects that omit `content`.
  // Returning a JSON string keeps results readable while remaining compatible
  // with both old and new hosts.
  // https://github.com/openclaw/openclaw/blob/936607ca221a2f0c37ad976ddefcd39596f54793/CHANGELOG.md?plain=1#L1144
  if (typeof data === "string") return data;
  try {
    return JSON.stringify(data, null, 2);
  } catch {
    return String(data);
  }
}

interface OpenClawPluginApi {
  pluginConfig?: unknown;
  logger: {
    info: (...args: unknown[]) => void;
    error: (...args: unknown[]) => void;
  };
  registerTool: (
    factory: ToolFactory | (() => AnyAgentTool[]),
    opts: { names: string[] }
  ) => void;
  on: (hookName: string, handler: (...args: unknown[]) => unknown, opts?: { priority?: number }) => void;
}

interface ToolContext {
  workspaceDir?: string;
  agentId?: string;
  sessionKey?: string;
  messageChannel?: string;
}

type ToolFactory = (ctx: ToolContext) => AnyAgentTool | AnyAgentTool[] | null | undefined;

interface AnyAgentTool {
  name: string;
  label: string;
  description: string;
  parameters: {
    type: "object";
    properties: Record<string, unknown>;
    required: string[];
  };
  execute: (_id: string, params: unknown) => Promise<unknown>;
}

function buildTools(backend: MemoryBackend): AnyAgentTool[] {
  const debouncer = new WriteDebouncer(backend);

  return [
    {
      name: "memory_store",
      label: "Store Memory",
      description:
        "Store a memory. Returns the stored memory with its assigned id. Writes are debounced (2s) and batched to reduce API calls.",
      parameters: {
        type: "object",
        properties: {
          content: {
            type: "string",
            description: "Memory content (required, max 50000 chars)",
          },
          source: {
            type: "string",
            description: "Which agent wrote this memory",
          },
          tags: {
            type: "array",
            items: { type: "string" },
            description: "Filterable tags (max 5 recommended)",
          },
          metadata: {
            type: "object",
            description: "Arbitrary structured data",
          },
        },
        required: ["content"],
      },
      async execute(_id: string, params: unknown) {
        try {
          const input = params as CreateMemoryInput;
          const result = await debouncer.enqueue(input);
          return jsonResult({ ok: true, data: result });
        } catch (err) {
          return jsonResult({
            ok: false,
            error: err instanceof Error ? err.message : String(err),
          });
        }
      },
    },

    {
      name: "memory_search",
      label: "Search Memories",
      description:
        "Search memories using hybrid vector + keyword search. Higher score = more relevant.",
      parameters: {
        type: "object",
        properties: {
          q: { type: "string", description: "Search query" },
          tags: {
            type: "string",
            description: "Comma-separated tags to filter by (AND)",
          },
          source: { type: "string", description: "Filter by source agent" },
          limit: {
            type: "number",
            description: "Max results (default 20, max 200)",
          },
          offset: { type: "number", description: "Pagination offset" },
          memory_type: {
            type: "string",
            description:
              "Comma-separated memory types to include (default: 'pinned,insight'). Options: pinned, insight, session.",
          },
        },
        required: [],
      },
      async execute(_id: string, params: unknown) {
        try {
          const input = (params ?? {}) as SearchInput;
          // Default to pinned+insight to exclude raw session dumps
          if (!input.memory_type) {
            input.memory_type = "pinned,insight";
          }
          const result = await backend.search(input);
          // Quality filtering: drop low-score noise + deduplicate near-identical results
          if (result.data) {
            result.data = filterSearchResults(result.data);
            // Cap individual memory content to prevent context blowup
            for (const mem of result.data) {
              if (mem.content && mem.content.length > MAX_SEARCH_RESULT_CONTENT) {
                mem.content =
                  mem.content.slice(0, MAX_SEARCH_RESULT_CONTENT) + "...[truncated]";
              }
            }
          }
          return jsonResult({ ok: true, ...result });
        } catch (err) {
          return jsonResult({
            ok: false,
            error: err instanceof Error ? err.message : String(err),
          });
        }
      },
    },

    {
      name: "memory_get",
      label: "Get Memory",
      description: "Retrieve a single memory by its id.",
      parameters: {
        type: "object",
        properties: {
          id: { type: "string", description: "Memory id (UUID)" },
        },
        required: ["id"],
      },
      async execute(_id: string, params: unknown) {
        try {
          const { id } = params as { id: string };
          const result = await backend.get(id);
          if (!result)
            return jsonResult({ ok: false, error: "memory not found" });
          return jsonResult({ ok: true, data: result });
        } catch (err) {
          return jsonResult({
            ok: false,
            error: err instanceof Error ? err.message : String(err),
          });
        }
      },
    },

    {
      name: "memory_update",
      label: "Update Memory",
      description:
        "Update an existing memory. Only provided fields are changed.",
      parameters: {
        type: "object",
        properties: {
          id: { type: "string", description: "Memory id to update" },
          content: { type: "string", description: "New content" },
          source: { type: "string", description: "New source" },
          tags: {
            type: "array",
            items: { type: "string" },
            description: "Replacement tags",
          },
          metadata: { type: "object", description: "Replacement metadata" },
        },
        required: ["id"],
      },
      async execute(_id: string, params: unknown) {
        try {
          const { id, ...input } = params as { id: string } & UpdateMemoryInput;
          const result = await backend.update(id, input);
          if (!result)
            return jsonResult({ ok: false, error: "memory not found" });
          return jsonResult({ ok: true, data: result });
        } catch (err) {
          return jsonResult({
            ok: false,
            error: err instanceof Error ? err.message : String(err),
          });
        }
      },
    },

    {
      name: "memory_delete",
      label: "Delete Memory",
      description: "Delete a memory by id.",
      parameters: {
        type: "object",
        properties: {
          id: { type: "string", description: "Memory id to delete" },
        },
        required: ["id"],
      },
      async execute(_id: string, params: unknown) {
        try {
          const { id } = params as { id: string };
          const deleted = await backend.remove(id);
          if (!deleted)
            return jsonResult({ ok: false, error: "memory not found" });
          return jsonResult({ ok: true });
        } catch (err) {
          return jsonResult({
            ok: false,
            error: err instanceof Error ? err.message : String(err),
          });
        }
      },
    },
  ];
}

const mnemoPlugin = {
  id: "mem9",
  name: "Mnemo Memory",
  description:
    "AI agent memory — server mode (mnemo-server) with hybrid vector + keyword search.",

  async register(api: OpenClawPluginApi) {
    const cfg = (api.pluginConfig ?? {}) as PluginConfig;
    const effectiveApiUrl = cfg.apiUrl ?? DEFAULT_API_URL;
    if (!cfg.apiUrl) {
      api.logger.info(`[mem9] apiUrl not configured, using default ${DEFAULT_API_URL}`);
    }

    const configuredApiKey = cfg.apiKey ?? cfg.tenantID;
    if (cfg.apiKey && cfg.tenantID) {
      api.logger.info("[mem9] both apiKey and tenantID set; using apiKey");
    } else if (cfg.tenantID) {
      api.logger.info("[mem9] tenantID is deprecated; treating it as apiKey for v1alpha2");
    }
    const registerTenant = async (agentName: string): Promise<string> => {
      const backend = new ServerBackend(effectiveApiUrl, "", agentName);
      const result = await backend.register();
      api.logger.info(
        `[mem9] *** Auto-provisioned apiKey=${result.id} *** Save this to your config as apiKey`
      );
      return result.id;
    };
    let registrationPromise: Promise<string> | null = null;
    const resolveAPIKey = (agentName: string): Promise<string> => {
      if (configuredApiKey) return Promise.resolve(configuredApiKey);
      if (!registrationPromise) {
        registrationPromise = registerTenant(agentName);
      }
      return registrationPromise;
    };

    api.logger.info("[mem9] Server mode (v1alpha2)");

    const hookAgentId = cfg.agentName ?? "agent";

    const factory: ToolFactory = (ctx: ToolContext) => {
      const agentId = ctx.agentId ?? cfg.agentName ?? "agent";
      const backend = new LazyServerBackend(
        effectiveApiUrl,
        () => resolveAPIKey(agentId),
        agentId,
      );
      return buildTools(backend);
    };

    api.registerTool(factory, { names: toolNames });

    // Register hooks with a lazy backend for lifecycle memory management.
    // Uses the default workspace/agent context for hook-triggered operations.
    const hookBackend = new LazyServerBackend(
      effectiveApiUrl,
      () => resolveAPIKey(hookAgentId),
      hookAgentId,
    );
    registerHooks(api, hookBackend, api.logger, {
      maxIngestBytes: cfg.maxIngestBytes,
      fallbackAgentId: hookAgentId,
    });
  },
};

const toolNames = [
  "memory_store",
  "memory_search",
  "memory_get",
  "memory_update",
  "memory_delete",
];

class LazyServerBackend implements MemoryBackend {
  private resolved: ServerBackend | null = null;
  private resolving: Promise<ServerBackend> | null = null;

  constructor(
    private apiUrl: string,
    private apiKeyProvider: () => Promise<string>,
    private agentId: string,
  ) {}

  private async resolve(): Promise<ServerBackend> {
    if (this.resolved) return this.resolved;
    if (this.resolving) return this.resolving;

    this.resolving = this.apiKeyProvider().then((apiKey) =>
      Promise.resolve().then(() => {
        this.resolved = new ServerBackend(this.apiUrl, apiKey, this.agentId);
        return this.resolved;
      })
    ).catch((err) => {
      this.resolving = null; // allow retry on next call
      throw err;
    });

    return this.resolving;
  }

  async store(input: CreateMemoryInput) {
    return (await this.resolve()).store(input);
  }
  async search(input: SearchInput) {
    return (await this.resolve()).search(input);
  }
  async get(id: string) {
    return (await this.resolve()).get(id);
  }
  async update(id: string, input: UpdateMemoryInput) {
    return (await this.resolve()).update(id, input);
  }
  async remove(id: string) {
    return (await this.resolve()).remove(id);
  }
  async ingest(input: IngestInput): Promise<IngestResult> {
    return (await this.resolve()).ingest(input);
  }
}
export default mnemoPlugin;
