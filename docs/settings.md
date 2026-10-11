# Settings

Settings (admins only) control how the agent thinks: which models it uses, its prompts, its tools, and the workspace itself. Every LLM setting lives here, in the database; the config file only supplies a new workspace's first values.

## Providers & keys

A **provider** is an endpoint that serves models:

- **Anthropic**, or **OpenAI-compatible** (OpenAI, Ollama, LM Studio, llama.cpp, vLLM, SGLang, and others) with a base URL.
- **API key:** paste it (stored encrypted, never shown again; needs `RESEARCH_AGENT_SECRETS_KEY` on the server) or name an environment variable set on the api and worker.
- **Local:** marks a provider as running on your own hardware; local-only mode uses only these.
- **Max simultaneous calls**, retries, extra headers, and how the server expects thinking and max-tokens parameters.

**Test** checks the connection; **Discover on <provider>** on the Models tab lists what the server offers. **Stored keys** lists encrypted keys by their last four characters.

## Models

Declare each model you use and what it can do: tools, structured output (JSON Schema), vision, thinking style, context window, and max output, plus default parameters and price per million tokens (for cost tracking). Declarations are used to validate routes and to decide whether the image tool is offered; they never switch features off for local models.

## Routes

Each **task** has a route: a primary model, an optional fallback (any provider), and parameters.

| Task | Used for |
|---|---|
| classify | Deciding which lanes an item fits |
| score | Relevance, intent, fit, and flags |
| draft | Drafts, plus the draft check |
| research | Research, deep research phases, memory bank files |
| summarize | Compacting long research context, knowledge section summaries |

The research route also sets the built-in tools allowed, the MCP servers it may use, max agent steps, time limit, and token budget. Each route shows whether it is usable right now and why not.

## Prompts

Every prompt is versioned. The shipped versions install automatically on upgrade. To change one, write a new version with a short "what changed" note and activate it; earlier versions stay for comparison and rollback. Prompts are templates: `{{.BrandName}}`, `{{.Brand}}`, and others are filled in at run time, and collected content is always wrapped as untrusted external content.

## Lanes

Per lane (inbound, media, thought leadership): the scoring weights (relevance, intent, fit, recency, reach, deadline), **Picks/day** for the daily featured list, **Fresh (h)** (how long an item stays relevant), and **Min score** (below it, no opportunity is created).

## MCP servers

Connect Model Context Protocol servers to give research extra tools. Tools are **denied by default**: a tool reaches the agent only if you allowlist it and the server marks it read-only and non-destructive. Only connect servers you trust, and never one that can post or send messages. Streamable HTTP servers work everywhere; stdio servers need their runtime inside the worker image.

## Memory bank

Connect a GitHub repository of Markdown docs about your product for research to use. See [memory-bank.md](memory-bank.md).

## Mode, embeddings & search

- **Local-only mode:** every model call goes to local providers. Collection and web search keep using the internet. Details below.
- **Embeddings:** provider, model, and dimensions for duplicate detection, brand matching, and semantic search. Any model works. **Discover** lists the provider's models (embedding models first); **Detect** embeds a short text and fills in the dimensions.
- **Search backend:** SearXNG (bundled, free), Brave, or Google Programmable Search, with country, language, safe search, and a monthly query budget for paid backends.

### How local-only mode works

"Local" is a flag on each provider (Providers & keys). The app trusts it; it doesn't check the provider's address. With local-only mode on:

- **Routes:** when a task resolves its route, any target (primary or fallback) on a provider not marked local is skipped. If nothing is left, the call fails with "local-only mode is on and this task has no local provider" instead of falling back to a hosted model. Routes show this as not usable.
- **Embeddings:** a hosted embeddings provider is refused, and the setting can't be turned on while one is selected.
- **Playground and the embeddings probe** refuse hosted models too.
- **Not affected:** collection, web search (SearXNG, Brave, Google), page fetching, GitHub sync for the memory bank, and MCP servers. Those reach the internet, but they don't send your prompts to a hosted model.

The setting is stored per workspace and read on every call, so it takes effect immediately. While it's on, Settings greys out hosted providers and their models (Providers, Models, Routes, Playground, embeddings) and the sidebar shows a **Local-only mode** badge to admins.

## Playground

Send a message to any model with your own system prompt, parameters, images, or JSON schema, and see the raw response stream. The quickest way to check a new provider.

## Activity

Recent model calls: task, model, tokens, latency, cost, and errors. Prompt text is never stored.

## Workspace & users

Rename the workspace (the brand name prompts use) and manage who can sign in. See [getting-started.md](getting-started.md#3-add-your-team).
