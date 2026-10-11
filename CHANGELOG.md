# Changelog

## v0.1.1 (2026-10-11)

Fixes from an end-to-end test of v0.1.0 on Kubernetes with a local model, plus dependency and security updates.

- **Research:** deep research gives each planned sub-question an even share of the gathering time, so a slow local model researches every sub-question instead of skipping the last ones.
- **Monitors:** Run now on an enabled monitor moves its next scheduled run to the following cron tick, so turning a monitor on and clicking Run now no longer starts two runs whose progress overlaps.
- **Playground:** messages with no text or an unknown role are rejected with a clear error instead of reaching the model empty.
- **Deploy tooling:** `make deploy-verify` reads logs from each deployment's newest pod, so right after a rollout it no longer reports the old pod's migrations or errors.
- **Dependencies:** Go and GitHub Actions updates, including security fixes in `moby/sys/user` (CVE-2026-61801), `quic-go`, and `moby/go-archive`. The `shadcn` CLI moved to the web app's dev dependencies: it generates components at development time and was never part of the running app.
- **Docs:** [Testing and tested configurations](docs/testing.md).

## v0.1.0 (2026-10-11)

First public release.

- **Collection:** monitors on a schedule over RSS/Atom (including Google Alerts), Hacker News, and web search (SearXNG, Brave, Google Programmable Search), with keyword matching, negative keywords, and per-source community rules.
- **Inbox:** filtering, de-duplication, LLM classification and scoring into lanes (inbound, media, thought leadership), a daily featured list, triage with dismissal reasons, and outcome tracking.
- **Drafts:** reply, pitch, and comment drafts in the brand's voice, checked against the brand's banned claims and each community's rules. Always labelled for human review; the app never posts.
- **Research:** chat and deep research with numbered citations, key claims checked against what the cited sources said, charts, live progress, Markdown export, an optional internal knowledge base synced from GitHub docs, and a generated memory bank.
- **LLM layer:** Anthropic and any OpenAI-compatible server as peers, per-task routes with fallback, local-only mode, versioned prompts, read-only MCP tools, and a playground.
- **Setup:** first-run setup in the browser, a Getting started checklist, brand packs (with a fictional example), users and roles.
- **Operations:** Docker Compose and Kubernetes manifests, encrypted secrets with key rotation, audit log, retention of collected items.

Known limitations are listed in [Deployment](docs/deployment.md#known-limitations).

