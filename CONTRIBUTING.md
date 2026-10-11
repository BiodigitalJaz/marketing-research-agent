# Contributing

Thanks for your interest. Bug reports, docs fixes, and pull requests are welcome.

## Ground rules

- **Read-only toward the outside world.** The app never posts, comments, messages, or otherwise writes to an external platform, and it never will. Pull requests that add such actions won't be merged. `scripts/check_no_posting.sh` enforces this in CI.
- **Hosted and local models are equals.** Don't add code paths that gate features on a provider being hosted or local; capabilities are declared per model in Settings.
- **Collected content is untrusted.** It's wrapped in `<external_content>` delimiters in prompts, URLs from it are fetched only through `internal/fetch`, and it can never change which tools the agent has.
- **Secrets stay secret.** Keys entered in Settings are never returned by the API, logged, or put into prompts or errors.

[CLAUDE.md](CLAUDE.md) lists the conventions in full (it's written for coding agents, and works for people too).

## Development setup

Requirements: Docker, Go (see `go.mod`), Node 22+, pnpm 9.

```sh
make tools          # sqlc, goose, staticcheck, golangci-lint, kubeconform, go-licenses
make dev            # postgres, migrate, api, worker, web, SearXNG via docker compose
make test           # Go unit and integration tests (testcontainers; needs Docker)
make lint           # go vet, staticcheck, golangci-lint, web lint, no-posting check
```

How the pieces fit: [docs/architecture.md](docs/architecture.md).

## Making a change

- **SQL** lives in `internal/store/queries/*.sql`; run `make sqlc` after editing. Every query on workspace data filters by `workspace_id`.
- **Schema changes** are a new goose migration in `migrations/`. Never edit one that has shipped.
- **Prompts** are versioned files in `internal/prompts/defaults`. To change one, add `task.vN+1.md`; don't edit old versions.
- **LLM structured outputs** need a JSON Schema and are validated before use.
- **API changes** go in `internal/httpapi/openapi.yaml`; run `make api-client` to regenerate the web client.
- **Config keys** are documented in `docs/configuration.md`.
- **Background work** is a River job in `internal/jobs`, enqueued in the same transaction as the data it depends on.
- **Tests** never call the network: use HTTP fixtures, the fake LLM provider (`internal/llm/fake`), and the testcontainers Postgres helper (`internal/testutil/pgtest`). Prefer table-driven tests.

## Pull requests

Keep them focused, describe what changed and how you tested it, and make sure `make lint` and `make test` pass. CI also checks generated code, Kubernetes manifests, and dependency licenses (`make check-licenses`).

## Licensing

This project is licensed under the [GNU Affero General Public License v3.0 only](LICENSE). By submitting a contribution, you agree that it is licensed under the same terms. There is no CLA and no sign-off requirement.

## Conduct

Everyone taking part is expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
