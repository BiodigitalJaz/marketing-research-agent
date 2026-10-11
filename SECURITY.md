# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub: **Security → Report a vulnerability** on this repository ([new advisory](https://github.com/BiodigitalJaz/marketing-research-agent/security/advisories/new)). Don't open a public issue.

Include what you found, how to reproduce it, and the impact you expect. You'll get an acknowledgment within a few days. Fixes are released as soon as practical, and reporters are credited in the advisory unless they'd rather not be.

## Supported versions

Security fixes go into the latest release and `main`.

## Scope

In scope: the api, worker, and web app in this repository, and their default deployment manifests. Areas that matter most:

- authentication, sessions, CSRF, and role checks;
- secrets handling (keys entered in Settings must never be returned, logged, or sent to a model);
- the fetcher's SSRF protection, which must refuse private and internal addresses for URLs found in collected content;
- prompt injection from collected content or MCP tool results changing which tools the agent can use;
- anything that would make the app post, comment, or message on an external platform.

Out of scope: vulnerabilities in third-party LLM providers, model servers, or MCP servers you connect, and issues that need an admin account to misconfigure the app deliberately.

## Running it safely

- Serve the web app over HTTPS and set `server.base_url` to the https URL so cookies are `Secure`.
- Set `RESEARCH_AGENT_SECRETS_KEY` and back it up; keep it out of version control.
- Expose only the web service. The api, SearXNG, and PostgreSQL should stay internal.
- Connect only MCP servers you trust, and allowlist only read-only tools.
