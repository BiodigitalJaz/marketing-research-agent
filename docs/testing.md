# Testing and tested configurations

## Automated tests

Every pull request and every push to `main` runs CI (`.github/workflows/ci.yml`):

| Job | What it checks |
|---|---|
| **go** | `go vet`, staticcheck, golangci-lint, that generated SQL code is current, and `go test -race ./...`. Integration tests run against PostgreSQL with pgvector in a throwaway container (testcontainers); model calls use a fake provider, and external sites use HTTP fixtures, so tests make no live network calls. |
| **web** | That the typed API client matches `openapi.yaml`, then lint, typecheck, and a production build |
| **k8s** | That both example overlays render and validate against the Kubernetes schemas |
| **licenses** | That no Go or web dependency has a license incompatible with AGPL-3.0-only |
| **no-posting** | That no code path can post, comment, or message on an external platform |

Run the same checks locally with `make test`, `make lint`, `make check-licenses`, and `make k8s-validate`.

## End-to-end checks

Before a release, a fresh install is taken through the whole workflow with a real model:

1. First-run setup with the example brand.
2. Connect a model: provider test, a route for every task, embeddings, and a Playground message.
3. Collection: turn on the starter monitor, run it, and wait until every collected item is processed into opportunities.
4. Drafts for the top opportunity, with the community and brand rule checks.
5. Research: a chat question and a deep research report, including the claim check.
6. Logs: no errors in the api and worker, and no pod restarts.

## Tested configurations

| Date | Version | Deployment | Database | Model (server) | Result |
|---|---|---|---|---|---|
| 2026-10-11 | v0.1.0 + dependency updates (`8e5db33`) | Kubernetes 1.33, public GHCR images | PostgreSQL 17.5, pgvector 0.8.0 (external) | Qwen3.8 27B, 8k context, thinking off (LM Studio); nomic-embed-text v1.5 | All steps passed: 35 items collected, 6 opportunities, drafts with rule warnings, deep research in 23 minutes with 15 citations (claim check: 6 supported, 4 unsupported). No errors or restarts. The three issues it found are fixed in v0.1.1. |
| 2026-10-11 | v0.1.0 release candidate (`38050a5`) | Docker Compose, fresh clone, built from source | Bundled PostgreSQL 17 with pgvector | Same as above | All steps passed: 23 items collected, 5 opportunities, drafts, chat research with 7 citations. No errors. |

Running a different setup? Results for other models, servers, and platforms are welcome in [issues](https://github.com/BiodigitalJaz/marketing-research-agent/issues).
