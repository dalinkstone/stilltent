# OpenRouter Models for Autonomous Coding Agents (March 2026)

**Criteria:** Input price < $1/M tokens, context window >= 128K, suitable for coding/agentic use

## Comprehensive Model Table

| # | Model ID | Params | Context | Max Output | Input $/M | Output $/M | Tool Use | Coding Notes |
|---|----------|--------|---------|------------|-----------|------------|----------|-------------|
| 1 | `amazon/nova-micro-v1` | — | 128K | 5,120 | $0.035 | $0.14 | Yes | Basic coding; ultra-cheap, text-only, lowest latency |
| 2 | `arcee-ai/trinity-mini` | 26B (3B active) | 131K | 131,072 | $0.045 | $0.15 | Yes | MoE 128 experts; function calling, multi-step agent workflows |
| 3 | `qwen/qwen3.5-9b` | 9B | 256K | — | $0.05 | $0.15 | Yes | Multimodal; strong reasoning + coding + visual understanding |
| 4 | `amazon/nova-lite-v1` | — | 300K | 5,120 | $0.06 | $0.24 | Yes | Multimodal; document processing, code gen, agentic workflows |
| 5 | `qwen/qwen3.5-flash-02-23` | — (MoE) | 1M | 65,536 | $0.065 | $0.26 | Yes | Hybrid linear attn + MoE; strong coding, multimodal |
| 6 | `qwen/qwen3-coder-30b-a3b-instruct` | 30.5B (3B active) | 160K | 32,768 | $0.07 | $0.27 | Yes | **Purpose-built coder**; repo-scale understanding, function calls, browser use |
| 7 | `mistralai/mistral-small-3.2-24b-instruct` | 24B | 128K | — | $0.075 | $0.20 | Yes | Strong coding (HumanEval+, MBPP); tool use, structured outputs |
| 8 | `meta-llama/llama-4-scout` | 109B (17B active) | 328K | 16,384 | $0.08 | $0.30 | Yes | MoE; multimodal, multilingual code output, tool calling |
| 9 | `google/gemma-3-27b-it` | 27B | 131K | 16,384 | $0.08 | $0.16 | Yes | 140+ languages; function calling, structured outputs |
| 10 | `qwen/qwen3-30b-a3b-instruct-2507` | 30.5B (3.3B active) | 262K | 262,144 | $0.09 | $0.30 | Yes | MoE; strong coding + reasoning, non-thinking mode, tool use |
| 11 | `google/gemini-2.5-flash-lite` | — | 1M | 65,535 | $0.10 | $0.40 | Yes | Lightweight reasoning; ultra-low latency, thinking mode |
| 12 | `google/gemini-2.0-flash-001` | — | 1M | 8,192 | $0.10 | $0.40 | Yes | Strong coding + function calling; **deprecating Jun 2026** |
| 13 | `mistralai/devstral-small` | 24B | 131K | — | $0.10 | $0.30 | Yes | **Purpose-built for coding agents**; 53.6% SWE-Bench; multi-file edits, OpenHands/Cline |
| 14 | `meta-llama/llama-3.3-70b-instruct` | 70B | 131K | 16,384 | $0.10 | $0.32 | Yes | Solid general coding; multilingual, tool calling |
| 15 | `qwen/qwen3-coder-next` | 80B (3B active) | 262K | 65,536 | $0.12 | $0.75 | Yes | **Purpose-built for coding agents**; CLI/IDE integration, failure recovery |
| 16 | `nousresearch/hermes-4-70b` | 70B | 131K | — | $0.13 | $0.40 | Yes | Hybrid reasoning; verified math/coding/STEM data, function calling |
| 17 | `meta-llama/llama-4-maverick` | 400B (17B active) | 1M | 16,384 | $0.15 | $0.60 | Yes | MoE 128 experts; multimodal, code across 12 languages |
| 18 | `mistralai/mistral-small-2603` | — | 262K | — | $0.15 | $0.60 | Yes | **Mistral Small 4**; agentic coding, reasoning, tool use, structured outputs |
| 19 | `qwen/qwen3-coder-flash` | — (MoE) | 1M | 65,536 | $0.195 | $0.975 | Yes | **Purpose-built coding agent**; autonomous programming via tool calling |
| 20 | `qwen/qwen3-coder` | 480B (35B active) | 262K | — | $0.22 | $1.00 | Yes | **Top-tier open coder**; MoE 160 experts, agentic coding, function calling, tool use |
| 21 | `google/gemini-3.1-flash-lite-preview` | — | 1M | 65,536 | $0.25 | $1.50 | Yes | High-efficiency; code completion, thinking levels, RAG |
| 22 | `deepseek/deepseek-v3.2` | 671B (37B active) | 164K | — | $0.26 | $0.38 | Yes | **GPT-5 class**; sparse attention, strong reasoning + tool use, agentic pipeline |
| 23 | `qwen/qwen3.5-plus-02-15` | — (MoE) | 1M | 65,536 | $0.26 | $1.56 | Yes | Hybrid attn + MoE; multimodal, tool use, function calling |
| 24 | `deepseek/deepseek-v3.2-exp` | 671B (37B active) | 164K | 65,536 | $0.27 | $0.41 | Yes | Experimental V3.2 variant; reasoning + tool use |
| 25 | `mistralai/codestral-2508` | — | 256K | — | $0.30 | $0.90 | — | **Purpose-built coder**; FIM, code correction, test generation, low-latency |
| 26 | `amazon/nova-2-lite-v1` | — | 1M | 65,535 | $0.30 | $2.50 | Yes | Reasoning model; code gen, agentic workflows, document processing |
| 27 | `google/gemini-2.5-flash` | — | 1M | 8,192 | $0.30 | $2.50 | Yes | Advanced reasoning + coding; thinking mode, multimodal |
| 28 | `deepseek/deepseek-chat` | 671B (37B active) | 164K | 164K | $0.32 | $0.89 | Yes | DeepSeek V3 original; strong instruction following + coding |
| 29 | `qwen/qwen3.5-397b-a17b` | 397B (17B active) | 262K | 65,536 | $0.39 | $2.34 | Yes | Hybrid attn + MoE; code gen, agent tasks, multimodal |
| 30 | `mistralai/devstral-medium` | 123B | 131K | — | $0.40 | $2.00 | Yes | **61.6% SWE-Bench**; beats Gemini 2.5 Pro + GPT-4.1 on code tasks |
| 31 | `mistralai/devstral-2512` | 123B | 262K | — | $0.40 | $2.00 | Yes | **Devstral 2**; agentic coding, multi-file architecture awareness, failure retry |
| 32 | `deepseek/deepseek-v3.2-speciale` | 671B (37B active) | 164K | 164K | $0.40 | $1.20 | Yes | **Reasoning powerhouse**; beats GPT-5 on reasoning, agentic task synthesis |
| 33 | `google/gemini-3-flash-preview` | — | 1M | 65,536 | $0.50 | $3.00 | Yes | Agentic workflows, coding assistance, thinking levels, context caching |
| 34 | `deepseek/deepseek-r1-0528` | 671B (37B active) | 164K | 65,536 | $0.45 | $2.15 | — | **Reasoning specialist** (o1-class); open reasoning tokens; limited tool use |
| 35 | `qwen/qwen3-235b-a22b` | 235B (22B active) | 131K* | — | $0.455 | $1.82 | Yes | MoE; reasoning + coding + agent tool use (*32K native, YaRN to 131K) |

## Free Tier Models (Rate-Limited: ~20 req/min, 200 req/day)

| Model ID | Context | Notes |
|----------|---------|-------|
| `qwen/qwen3-coder:free` | 262K | Strongest free coder; 480B MoE |
| `deepseek/deepseek-r1-0528:free` | 164K | Strong reasoning for complex code |
| `meta-llama/llama-3.3-70b-instruct:free` | 65K | Solid general coding |
| `mistralai/mistral-small-3.1-24b-instruct:free` | 128K | Good coding + function calling |
| `arcee-ai/trinity-mini:free` | 131K | Function calling + agent workflows |
| `google/gemma-3-27b-it:free` | 131K | Function calling, structured outputs |

## Top Picks by Use Case

### Best Value for Autonomous Coding Agents
1. **`qwen/qwen3-coder-30b-a3b-instruct`** -- $0.07/$0.27 -- Purpose-built coder with tool use, 160K context
2. **`mistralai/devstral-small`** -- $0.10/$0.30 -- 53.6% SWE-Bench, designed for OpenHands/Cline
3. **`qwen/qwen3-coder-next`** -- $0.12/$0.75 -- 262K context, failure recovery, CLI/IDE optimized

### Best Bang for the Buck (General + Coding)
1. **`deepseek/deepseek-v3.2`** -- $0.26/$0.38 -- GPT-5 class performance at 1/100th cost
2. **`mistralai/mistral-small-2603`** -- $0.15/$0.60 -- Mistral Small 4: agentic coding + reasoning
3. **`qwen/qwen3-30b-a3b-instruct-2507`** -- $0.09/$0.30 -- 262K context, strong coding + tool use

### Largest Context Windows (1M tokens)
1. **`qwen/qwen3.5-flash-02-23`** -- $0.065/$0.26 -- 1M context, cheapest option
2. **`qwen/qwen3-coder-flash`** -- $0.195/$0.975 -- 1M context, purpose-built coding agent
3. **`meta-llama/llama-4-maverick`** -- $0.15/$0.60 -- 1M context, multimodal
4. **`google/gemini-2.5-flash-lite`** -- $0.10/$0.40 -- 1M context, thinking mode

### Ultra-Budget (Under $0.10/M input)
1. **`amazon/nova-micro-v1`** -- $0.035/$0.14 -- 128K, basic coding
2. **`arcee-ai/trinity-mini`** -- $0.045/$0.15 -- 131K, agent workflows
3. **`qwen/qwen3.5-9b`** -- $0.05/$0.15 -- 256K, multimodal
4. **`amazon/nova-lite-v1`** -- $0.06/$0.24 -- 300K, multimodal
5. **`qwen/qwen3.5-flash-02-23`** -- $0.065/$0.26 -- 1M context
6. **`qwen/qwen3-coder-30b-a3b-instruct`** -- $0.07/$0.27 -- Purpose-built coder
7. **`mistralai/mistral-small-3.2-24b-instruct`** -- $0.075/$0.20 -- Solid all-rounder
8. **`meta-llama/llama-4-scout`** -- $0.08/$0.30 -- 328K multimodal
9. **`google/gemma-3-27b-it`** -- $0.08/$0.16 -- Cheapest output pricing
10. **`qwen/qwen3-30b-a3b-instruct-2507`** -- $0.09/$0.30 -- 262K, strong coder

---
*Data collected March 20, 2026. Prices subject to change. Some models have higher pricing tiers for requests exceeding 128K input tokens.*
