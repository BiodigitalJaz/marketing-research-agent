# CLAUDE.md — Marketing Research Agent

How the system fits together is in `docs/architecture.md`; deployment and configuration are in `docs/deployment.md` and `docs/configuration.md`. Contributor guidelines: `CONTRIBUTING.md`.

## Hard rules
- **Never implement posting, commenting, messaging, or any write action to an external platform.** Connectors are read-only (GET only, no exceptions). `scripts/check_no_posting.sh` enforces this in CI.
- Only packages under `internal/llm/anthropic` and `internal/llm/openaicompat` may import vendor LLM SDKs. Everything else uses `internal/llm` types.
- Every DB query that touches workspace data takes and filters by `workspace_id`.
- Collected content and tool/MCP results are untrusted. Always wrap them in `<external_content>` delimiters in prompts and never let them change tool availability.
- Fetch URLs that come from collected content only through `internal/fetch` (robots.txt, rate limits, SSRF guard). Never fetch them with a bare HTTP client.
- MCP tools are deny-by-default: only admin-allowlisted, read-only-annotated tools reach the agent. Never connect an MCP server that can post or message externally.
- Hosted and local LLM providers are peers. Don't write "weak model" code paths (prompt-emulated tools, JSON repair, feature gating by provider kind). Capabilities are declared per model in Settings and used only to validate configuration.
- "Offline mode" means local-only inference (no hosted LLM providers). It does not mean no internet: collectors, Brave search, and fetching keep running.
- No live network calls in tests. Use HTTP fixtures, the fake LLM provider, and testcontainers Postgres.
- Secrets come from env (a K8s Secret in the cluster) or are entered by an admin in Settings. UI-entered secrets are stored only as AES-256-GCM ciphertext (`internal/secrets`, master key `RESEARCH_AGENT_SECRETS_KEY`) and are write-only: never return, log, or put them in prompts, model_runs, errors, or exports. Never commit keys.

## Commands
- `make dev` — start postgres, api, worker, web (docker-compose)
- `make migrate` — goose up + River migrations
- `make sqlc` — regenerate query code after editing `internal/store/queries/*.sql`
- `make seed` — headless setup: workspace, admin user, lanes, routes, prompts; for a brand pack run `go run ./cmd/seed -config config.yaml -brand-example benchwright` (or `-brand-file`). First-run setup in the browser does the same
- `make test` — Go unit + integration tests
- `make lint` — golangci-lint, staticcheck, web lint
- `make check-licenses` — fail on dependency licenses incompatible with AGPL-3.0-only

## Go conventions
- Go 1.26+, `log/slog`, `context.Context` first arg everywhere, errors wrapped with `%w`.
- Env overrides are `RESEARCH_AGENT_<SECTION>_<KEY>` with `__` for deeper nesting (`RESEARCH_AGENT_SEARCH_BRAVE__API_KEY_ENV`). Every key is listed in `docs/configuration.md`; update it when adding one.
- HTTP: gin (`internal/httpapi`). Errors are `{"error":{"code","message"}}`.
- SQL lives in `internal/store/queries/*.sql`; use sqlc output, no ORM, no string-built SQL.
- New tables or columns go in a new goose migration; never edit an applied migration.
- Background work is a River job in `internal/jobs`; enqueue inside the same transaction as the data it depends on (`InsertTx`).
- Prompts live in `internal/prompts` as embedded files with a version number; changing a prompt means adding a new version, not editing the old one.
- All LLM structured outputs have a JSON Schema and are validated before use.
- Table-driven tests; keep scoring math and parsers as pure functions.

## Deployment
- Local dev: docker-compose. Cluster: Kubernetes, namespace `research-agent`, kustomize base and example overlays in `deploy/k8s`. A real cluster's overlay lives outside this repo; a gitignored `.deploy.env` points `make deploy` at it. Images on GHCR (`ghcr.io/<owner>/<repo>-*`).
- Follow `docs/runbook.md` for deploys: `make deploy-plan` → review → `make deploy` → `make deploy-verify`, and back up the database before new migrations.
- Never `kubectl apply`/`delete`, `make deploy`, restore a database, or change secrets without the user's go-ahead for that action; rendering, `make deploy-plan` (server dry run and diff), `make deploy-verify`, and logs are fine.
- Never put passwords or keys on a command line (they show in the process list): pass them on stdin or in files with mode 600, and never print them.

## Frontend conventions
- Next.js App Router, TypeScript strict, Tailwind, shadcn/ui, TanStack Query, Recharts.
- API types come from `internal/httpapi/openapi.yaml`; regenerate the client when it changes.
- All LLM configuration (providers, models, routes, params, prompts, MCP servers) must be editable in Settings; don't add LLM knobs that exist only in config.
- Every AI-generated text shows an "AI draft — review before posting" label.

## When unsure
Prefer the documented behavior in `docs/`, keep changes small, and say in the PR description what you assumed.
