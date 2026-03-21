import type { MemoryBackend } from "./backend.js";
import type {
  Memory,
  StoreResult,
  SearchResult,
  CreateMemoryInput,
  UpdateMemoryInput,
  SearchInput,
  IngestInput,
  IngestResult,
} from "./types.js";

type ProvisionMem9sResponse = {
  id: string;
};

// Retry config
const RETRYABLE_STATUS_CODES = new Set([502, 503, 504]);
const MAX_RETRIES = 3;
const BASE_BACKOFF_MS = 500;

// Simple TTL cache for search results
interface CacheEntry<T> {
  data: T;
  expiresAt: number;
}

const SEARCH_CACHE_TTL_MS = 30_000; // 30 seconds — long enough to cover a full agent turn
const SEARCH_CACHE_MAX_ENTRIES = 20; // cap to bound memory on small droplets

export class ServerBackend implements MemoryBackend {
  private baseUrl: string;
  private apiKey: string;
  private agentName: string;
  private searchCache = new Map<string, CacheEntry<unknown>>();

  constructor(
    apiUrl: string,
    apiKey: string,
    agentName: string,
  ) {
    this.baseUrl = apiUrl.replace(/\/+$/, "");
    this.apiKey = apiKey;
    this.agentName = agentName;
  }

  private getCached<T>(key: string): T | undefined {
    const entry = this.searchCache.get(key);
    if (!entry) return undefined;
    if (Date.now() > entry.expiresAt) {
      this.searchCache.delete(key);
      return undefined;
    }
    return entry.data as T;
  }

  private setCache<T>(key: string, data: T): void {
    const now = Date.now();
    // Evict expired entries on every write to keep cache bounded
    for (const [k, v] of this.searchCache) {
      if (now > v.expiresAt) this.searchCache.delete(k);
    }
    // Hard cap: if still at limit after expiry sweep, evict oldest entry
    if (this.searchCache.size >= SEARCH_CACHE_MAX_ENTRIES) {
      const oldestKey = this.searchCache.keys().next().value;
      if (oldestKey !== undefined) this.searchCache.delete(oldestKey);
    }
    this.searchCache.set(key, { data, expiresAt: now + SEARCH_CACHE_TTL_MS });
  }

  async register(): Promise<ProvisionMem9sResponse> {
    const resp = await fetch(this.baseUrl + "/v1alpha1/mem9s", {
      method: "POST",
      signal: AbortSignal.timeout(8_000),
    });

    if (!resp.ok) {
      const body = await resp.text();
      throw new Error(`mem9s provision failed (${resp.status}): ${body}`);
    }

    const data = (await resp.json()) as ProvisionMem9sResponse;
    if (!data?.id) {
      throw new Error("mem9s provision did not return API key");
    }

    this.apiKey = data.id;
    return data;
  }

  private memoryPath(path: string): string {
    if (!this.apiKey) {
      throw new Error("API key is not configured");
    }
    return `/v1alpha2/mem9s${path}`;
  }

  async store(input: CreateMemoryInput): Promise<StoreResult> {
    return this.request<StoreResult>("POST", this.memoryPath("/memories"), input);
  }

  async search(input: SearchInput): Promise<SearchResult> {
    const params = new URLSearchParams();
    if (input.q) params.set("q", input.q);
    if (input.tags) params.set("tags", input.tags);
    if (input.source) params.set("source", input.source);
    if (input.limit != null) params.set("limit", String(input.limit));
    if (input.offset != null) params.set("offset", String(input.offset));
    if (input.memory_type) params.set("memory_type", input.memory_type);

    const qs = params.toString();
    const url = `${this.memoryPath("/memories")}${qs ? "?" + qs : ""}`;

    // Normalize cache key: lowercase + trim the query to deduplicate near-identical searches
    const normalizedQs = new URLSearchParams(params);
    if (input.q) normalizedQs.set("q", input.q.toLowerCase().trim());
    const cacheKey = `search:${normalizedQs.toString()}`;
    const cached = this.getCached<SearchResult>(cacheKey);
    if (cached) return cached;

    const raw = await this.request<{
      memories: Memory[];
      total: number;
      limit: number;
      offset: number;
    }>("GET", url);
    const result: SearchResult = {
      data: raw.memories ?? [],
      total: raw.total,
      limit: raw.limit,
      offset: raw.offset,
    };

    this.setCache(cacheKey, result);
    return result;
  }

  async get(id: string): Promise<Memory | null> {
    try {
      return await this.request<Memory>("GET", this.memoryPath(`/memories/${id}`));
    } catch {
      return null;
    }
  }

  async update(id: string, input: UpdateMemoryInput): Promise<Memory | null> {
    try {
      return await this.request<Memory>("PUT", this.memoryPath(`/memories/${id}`), input);
    } catch {
      return null;
    }
  }

  async remove(id: string): Promise<boolean> {
    try {
      await this.request("DELETE", this.memoryPath(`/memories/${id}`));
      return true;
    } catch {
      return false;
    }
  }

  async ingest(input: IngestInput): Promise<IngestResult> {
    return this.request<IngestResult>("POST", this.memoryPath("/memories"), input);
  }

  private async requestRaw(
    method: string,
    path: string,
    body?: unknown
  ): Promise<Response> {
    const url = this.baseUrl + path;
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      "X-Mnemo-Agent-Id": this.agentName,
      "X-API-Key": this.apiKey,
    };
    // Use method-appropriate timeouts: writes get more time than reads
    const timeoutMs = (method === "GET" || method === "DELETE") ? 5_000 : 10_000;
    return fetch(url, {
      method,
      headers,
      body: body != null ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(timeoutMs),
    });
  }

  private async request<T>(
    method: string,
    path: string,
    body?: unknown
  ): Promise<T> {
    let lastError: Error | undefined;

    for (let attempt = 0; attempt <= MAX_RETRIES; attempt++) {
      try {
        const resp = await this.requestRaw(method, path, body);

        if (resp.status === 204) {
          return undefined as T;
        }

        // Retry on transient server errors (502, 503, 504)
        if (RETRYABLE_STATUS_CODES.has(resp.status) && attempt < MAX_RETRIES) {
          const backoff = BASE_BACKOFF_MS * Math.pow(2, attempt);
          await new Promise((r) => setTimeout(r, backoff));
          continue;
        }

        const data = await resp.json();
        if (!resp.ok) {
          throw new Error((data as { error?: string }).error || `HTTP ${resp.status}`);
        }
        return data as T;
      } catch (err) {
        lastError = err instanceof Error ? err : new Error(String(err));
        // Retry on network/timeout errors, but not on application-level errors
        const isNetworkError = lastError.name === "TimeoutError"
          || lastError.name === "AbortError"
          || lastError.message.includes("fetch failed")
          || lastError.message.includes("ECONNREFUSED");
        if (!isNetworkError || attempt >= MAX_RETRIES) throw lastError;
        const backoff = BASE_BACKOFF_MS * Math.pow(2, attempt);
        await new Promise((r) => setTimeout(r, backoff));
      }
    }

    throw lastError ?? new Error("request failed after retries");
  }
}
