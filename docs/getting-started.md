# Getting started

This walks a new install from first boot to a working inbox. For running the stack itself, see [deployment.md](deployment.md).

## 1. Claim the install

On first start with an empty database, the API prints a one-time **setup token**:

```
level=WARN msg="first-run setup is open: no users exist yet. Open the setup page and enter this one-time token" url=http://localhost:3000/setup setup_token=…
```

Find it with `docker compose logs api | grep setup_token` (Compose) or `kubectl -n research-agent logs deploy/api -c api | grep setup_token` (Kubernetes). To choose the token yourself, set `RESEARCH_AGENT_SETUP_TOKEN` on the api before it starts; it is then not printed.

Open the app. With no users it sends you to **/setup**. Enter:

- the setup token;
- your brand or workspace name (prompts call your brand by this name);
- your name, email, and a password of at least 12 characters;
- optionally, **Start with an example brand** to fill the Brand page with Benchwright, a fictional LLM evaluation tool, and see how everything fits together. Its niche is busy on Hacker News, so the first monitor run finds opportunities quickly.

**Create workspace** makes you the admin, sets up the workspace's defaults (lanes, prompts, starter sources, and model routes from the config file), and signs you in. Setup works once: the token is deleted, and the page redirects to sign-in from then on. Each API restart replaces an unused token, so use the one from the most recent log.

Prefer no UI? `seed` does the same from the command line; see [configuration.md](configuration.md#environment-variables).

## 2. Work through "Getting started"

After setup you land on **Getting started**, a checklist that stays in the sidebar until every step is done. Each step opens the page where it's done.

| Step | Where | Done when |
|---|---|---|
| **Connect a language model** | Settings → Providers & keys, Models, Routes | Every task (classify, score, draft, research, summarize) routes to a model, and a model has answered. Use **Test** on a provider, then send a message in **Playground** to confirm. |
| **Set up embeddings** | Settings → Mode, embeddings & search | An embeddings model is configured and something has been embedded. Optional but recommended. |
| **Choose a web search backend** | Settings → Mode, embeddings & search | SearXNG (bundled, free) has a URL, or Brave / Google Programmable Search has a key. |
| **Describe your brand** | Brand → Profile | The profile lists at least one service. Fill in tone, the claims you never make, and targeting too; scoring and drafts depend on them. |
| **Add keywords to track** | Brand → Keywords | At least one tracked keyword. |
| **Turn on a monitor** | Monitors & sources | A monitor with sources is enabled. The starter monitor (Hacker News and web search) is created disabled. |

### Choosing models

Any OpenAI-compatible server works (Ollama, LM Studio, llama.cpp, vLLM, SGLang), as do the Anthropic and OpenAI APIs. One model can serve every task. The model needs tool calling and JSON-schema structured output; declare its context window and capabilities under **Models**. A 14B–32B local model with at least 8k context handles everything; more context makes research better. The seeded routes use Claude with Ollama-named local fallbacks; with only a local server, point each of the five routes at your model. See [Choosing models](models.md) for recommendations, hardware, and the settings local models need (thinking off, max tokens).

API keys entered in Settings are stored encrypted, which needs `RESEARCH_AGENT_SECRETS_KEY` on the api and worker. Without it, reference an environment variable name instead.

### Describing your brand

Write the profile the way you'd brief a new teammate: what you offer, who it's for, who it isn't for, how you sound, and the claims you never make. You can also write it as a [brand pack](brand-packs.md) and import it.

## 3. Add your team

**Settings → Workspace & users** (admins) lists everyone in the workspace. **Add a person** creates their account with a temporary password; the app never sends email, so share the password with them yourself. They change it under **Account** (click your email at the bottom of the sidebar). The same page renames the workspace.

| Role | Can |
|---|---|
| Viewer | Read the inbox, opportunities, research, reports, brand, and memory bank |
| Editor | Also triage and draft, run research, and edit the brand and monitors |
| Admin | Also manage sources, every Settings tab, and users |

Removing someone takes them out of the workspace and signs them out; their account stays so their past work keeps its attribution. A workspace always keeps at least one admin. Admins can reset a password (it signs the person out) unless that person also belongs to another workspace.

## 4. First results

Once a monitor is enabled, it collects on its schedule (every 30 minutes for the starter). Items are filtered, de-duplicated, classified, and scored; anything above a lane's minimum score becomes an opportunity. The next morning (after 06:00 in `inbox.timezone`) the best ones are featured in the inbox with drafts ready. **Run now** on a monitor collects immediately.

Then read [Using the inbox](inbox.md).
