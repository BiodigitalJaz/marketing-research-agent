# Configuration

Marketing Research Agent has two kinds of configuration:

1. **Process configuration**: a YAML file plus environment variables, read at startup by every binary. It covers things the process needs before it can reach the database: listen address, database URL, worker sizes, logging.
2. **Workspace settings**: everything about LLMs, search, prompts, lanes, and sources. These live in the database and are edited in **Settings** in the UI. A few YAML keys only provide the *initial* values when first-run setup (or the seed command) creates a workspace. After that, the UI is the source of truth.

## Loading order

1. Built-in defaults
2. The YAML file: `-config <path>`, else `$RESEARCH_AGENT_CONFIG`, else `./config.yaml`. The file is required. `${VAR}` and `${VAR:-default}` are expanded from the environment.
3. Environment overrides: `RESEARCH_AGENT_<SECTION>_<KEY>`. Use a double underscore for deeper nesting: `RESEARCH_AGENT_SEARCH_BRAVE__API_KEY_ENV=MY_KEY` sets `search.brave.api_key_env`. Lists (`providers`) and `routes` can only be set in YAML.

Invalid configuration stops the process with a list of every problem. At startup the process also warns about prefixed variables that match no key (usually a single underscore where nesting needs a double one), deprecated names, and keys that are no longer used. `config.example.yaml` is a complete, commented example.

## Process settings

| Key | Default | Notes |
|---|---|---|
| `server.addr` | `:8080` | API listen address |
| `server.base_url` | `http://localhost:3000` | The URL users open. If it starts with `https://`, cookies are `Secure` with the `__Host-` prefix. **Set this when serving over TLS.** |
| `server.shutdown_timeout` | `15s` | API graceful shutdown |
| `database.url` | none | **Required.** Postgres URL; `?pool_max_conns=N` tunes the pool |
| `worker.max_workers` | `10` | Concurrency of the `default` queue (collection, housekeeping) |
| `worker.pipeline_workers` | `2` | Concurrency of the `pipeline` queue (classify, score, draft). Keep low when one local GPU serves the LLM. |
| `worker.shutdown_timeout` | `30s` | How long the worker drains jobs on SIGTERM |
| `log.level` | `info` | `debug`, `info`, `warn`, `error` |
| `log.format` | `json` | `json` or `text` |
| `retention.raw_items_days` | `90` | Days to keep collected items and fetched pages. A daily job deletes older ones, except those behind a saved, snoozed, or acted-on opportunity and those a report cites. |
| `inbox.timezone` | `UTC` | IANA zone. The daily inbox is built after 06:00 in this zone. |

## Initial workspace values

First-run setup and the seed command copy these into a new workspace. Change them later in **Settings**.

| Key | Settings tab | Notes |
|---|---|---|
| `providers[]` (`id`, `kind`, `base_url`, `api_key_env`, `local`) | Providers & keys | `kind` is `anthropic` or `openai_compat`. `openai_compat` needs `base_url`. `anthropic` can't be `local`. |
| `routes.<task>` (`provider`, `model`, `fallback`) | Routes | Tasks: `classify`, `score`, `draft`, `research`, `summarize` |
| `embeddings` (`provider`, `model`, `dimensions`) | Mode, embeddings & search | Any model and dimension; search is exact |
| `search.backend` | Mode, embeddings & search | `searxng` (default), `brave`, `google_pse` |
| `search.searxng_url` | Mode, embeddings & search | Default `http://searxng:8080` |
| `search.brave.api_key_env` | Mode, embeddings & search | Required when the backend is `brave` |
| `mode.offline` | Mode, embeddings & search | Local-only inference: hosted LLM providers are disabled. Collection and web search keep running. |

## Environment variables

| Variable | Used by | Purpose |
|---|---|---|
| `RESEARCH_AGENT_CONFIG` | all | Path to the YAML config. Images set `/etc/marketing-research-agent/config.yaml`. |
| `RESEARCH_AGENT_DATABASE_URL` | all | Overrides `database.url` |
| `RESEARCH_AGENT_SECRETS_KEY` | api, worker | Master key for encrypted secrets: base64 of 32 random bytes (`openssl rand -base64 32`). Without it, keys can't be entered in Settings. |
| `RESEARCH_AGENT_SECRETS_KEY_PREVIOUS` | api, worker | Previous master key during rotation |
| `RESEARCH_AGENT_SETUP_TOKEN` | api | Optional. The first-run setup token; when unset, the api generates one and logs it. Only used while no user exists. |
| `RESEARCH_AGENT_ADMIN_EMAIL`, `RESEARCH_AGENT_ADMIN_PASSWORD`, `RESEARCH_AGENT_ADMIN_NAME` | seed | Admin account for headless setup. The password must be at least 12 characters and is only used when creating the user. |
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `BRAVE_API_KEY` | api, worker | Default env names referenced by `api_key_env` |
| `LOCAL_LLM_BASE_URL` | all | Expanded into the example `local` provider's base URL when a workspace is created; change it later in Settings |
| `SEARXNG_URL` | seed | Expanded into `search.searxng_url` by the example config |
| `SEARXNG_SECRET` | SearXNG | SearXNG's own secret |
| `API_URL` | web (**build time**) | Where the web server proxies `/api/*`. Default `http://api:8080` in the image. |
| *any name you set in Settings* | api, worker | Provider keys, search keys, MCP headers, and GitHub tokens can reference an env var instead of a stored secret. Set it on **both** the api and the worker. |

The seed command also takes flags: `-workspace NAME`, `-brand-file PATH` (a [brand pack](brand-packs.md)), and `-brand-example NAME` (an embedded example such as `benchwright`).

## Secrets

Keys entered in Settings are encrypted with AES-256-GCM under `RESEARCH_AGENT_SECRETS_KEY`, stored with only their last four characters visible, and never returned by the API, logged, or put in prompts. Every place that takes a key also accepts an env var name instead. Use that when you'd rather manage keys in your platform's secret store.
