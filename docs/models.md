# Choosing models

Every task the agent runs has its own **route** in **Settings → Routes**: a model, an optional fallback, and parameters. You can use one model for everything, or match each task to a model that fits it. Hosted and local models are peers; the app has no reduced mode for local models.

## What each task needs

| Task | When it runs | What it needs | Volume |
|---|---|---|---|
| **classify** | Each collected item that passes the keyword filter | Structured output (JSON Schema); short input and output | Highest: one call per new item |
| **score** | Each item classified as an opportunity | Structured output; judgment against the brand profile | High |
| **draft** | Opportunities featured in the daily inbox, and on demand | Good writing that follows the brand's voice and banned claims | Moderate |
| **summarize** | During research, to condense long tool results | Faithful, short summaries | Moderate |
| **research** | Chat questions and deep research reports | Tool calling, structured output (plan and claim check), and the most context | Low count, most tokens per run |
| **embeddings** | New items, brand entities, and searches | An OpenAI-compatible `/v1/embeddings` endpoint | One call per item |

Three capabilities matter:

- **Tool calling** for research. The agent searches, fetches pages, and reads collected items through tools.
- **JSON-schema structured output** for classify, score, and research planning and verification. Outputs are validated before use, and a model that can't follow a schema fails these tasks.
- **Context window:** at least 8k tokens. Research gets noticeably better at 16k–32k, because more evidence fits before older tool results are summarized.

## Recommendations

| Setup | classify, score, summarize | draft, research | Notes |
|---|---|---|---|
| **Hosted (simplest)** | Claude Haiku 4.5 (seeded default) | Claude Sonnet 5.5 (seeded default) | No hardware. Research is the main cost; set prices in Settings → Models to track spending in Settings → Activity. |
| **Local, one GPU** | A 14B–32B instruction model | The same model | Tested: Qwen3.8 27B (Unsloth build) on LM Studio for every task, with 8k context. See the settings below. |
| **Mixed** | A local 14B–32B model | Claude Sonnet 5.5 | Keeps the high-volume tasks free and local, and pays only for research and drafts. |
| **Local only** | A local model | A local model | Turn on local-only mode in Settings so no call reaches a hosted provider, even as a fallback. |

**Seeded routes** point at Claude (Sonnet 5.5 for draft and research, Haiku 4.5 for the rest) with local fallbacks named for Ollama (`qwen3:14b`, `qwen3:32b`). Without an Anthropic key, or with another local server, set each of the five routes to a model your server serves.

Any OpenAI-compatible server works: Ollama, LM Studio, llama.cpp, vLLM, or SGLang. OpenAI's API works with a current model that supports structured outputs.

**Embeddings:** a small model such as `nomic-embed-text` (v1.5, 768 dimensions) runs well on CPU. Without embeddings, near-duplicate detection, brand similarity, portfolio matching, and semantic search are skipped.

### Hardware for local models

Rough guide for 4-bit quantized models with an 8k context:

| Model size | GPU memory |
|---|---|
| 14B | about 10–12 GB |
| 27B–32B | about 20–24 GB |

More context needs more memory. On a single GPU, set the provider's **max simultaneous calls** to 1 and keep `worker.pipeline_workers` at 2 or lower, so requests queue instead of competing. CPU-only inference works for trying the app (`docker compose --profile ollama up -d`) but is slow for real use.

## Settings that matter for local models

These are the settings used in testing with Qwen3.8 27B and an 8k context:

| Setting | Where | Value | Why |
|---|---|---|---|
| Thinking off | Each route's **Extra request fields** | `reasoning_effort: none` (or the server's equivalent, such as `{"chat_template_kwargs": {"enable_thinking": false}}`) | With thinking on, calls can run for minutes and time out. |
| Max tokens | Each route | 3072 | Leaves room for the prompt inside an 8k window. Raise it with a larger context. |
| Context window | Settings → Models | The context your server actually loads (e.g. 8192) | Research uses it to decide when to summarize older tool results. |
| Max simultaneous calls | Settings → Providers & keys | 1 on a single GPU | Avoids timeouts from parallel requests. |

Use **Discover on \<provider\>** in Settings → Models to list the models your server offers, and the **Playground** to send a test message to each routed model.

## What to expect

- **Speed:** with a local 27B model, classifying and scoring a batch of new items takes minutes, and a deep research report can take 15–25 minutes. Hosted models are much faster.
- **Research quality depends on search as much as on the model.** The bundled SearXNG uses only engines whose terms allow automated queries (encyclopedias, academic indexes, Stack Exchange, GitHub). For vendor documentation, blogs, and news, configure Brave or Google Programmable Search in Settings, or ask questions your collected items can answer.
- **Review citations.** Deep research checks each key claim against excerpts of its cited sources and lists unsupported claims separately, but the check is a model's judgment, and a small model is a looser judge.

See also [Settings](settings.md) for every model option and [Deployment](deployment.md#sizing) for sizing the rest of the stack.
