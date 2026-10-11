# The memory bank

The memory bank gives the research agent an internal reference for questions about your own product: its product, architecture, security, infrastructure, decisions, and status. It is modelled on [Cline's memory bank](https://docs.cline.bot/best-practices/memory-bank).

It has two layers:

1. **Internal docs (ground truth).** Markdown files read from a repository's **main branch**, by default the `docs/` directory. Each file is split at its headings into sections; each section can be searched and cited. Citations link to the section on GitHub at the exact commit that was read.
2. **Six core files (generated).** Concise summaries built from the docs, in Cline's structure:

| File | What it holds |
|---|---|
| `projectbrief` | The foundation: what the project is, its requirements, goals, scope |
| `productContext` | Why it exists, who it's for, how it should work, positioning |
| `systemPatterns` | Architecture, key decisions, security model, patterns |
| `techContext` | Stack, infrastructure and deployment, constraints |
| `activeContext` | Current focus, recent changes, active decisions, next steps |
| `progress` | What works, what's left, known issues, how it evolved |

## How it stays current

- Syncing is on demand: **Sync now** in Settings checks the branch's latest commit. If nothing changed, that one request is all it costs.
- When the commit changed, only files whose content changed are re-read; deleted files are removed. Unchanged files are never rewritten, so a sync never throws away memory bank work. New sections are embedded for semantic search.
- After a change, the memory bank is updated incrementally. Changed sections are summarized, each changed document gets a digest (which also records the core files it informs), and only the core files whose source documents changed are regenerated. `projectbrief` is the foundation, so when it changes the files that build on it are regenerated too.
- **One thing at a time.** A sync and a memory bank build never run together for a workspace. While either is queued or running, Sync now and Update memory bank are disabled, and the API answers 409 "busy".
- Section summaries use the **summarize** route and digests and core files use the **research** route (Settings → Routes). A first build reads every document and can take a few hours on a small local model. It works in slices and continues across job runs; Settings shows "building" until it finishes.

Only Markdown files are read. Use the exclude list (globs relative to the docs directory, `dir/**` for a directory) to keep out anything that shouldn't inform research, such as raw data, drafts, or internal results.

## Setting it up

1. Create a **fine-grained GitHub token** with access to the one repository and **read-only "Contents"** permission. Nothing else is needed; the app only reads.
2. In **Settings → Memory bank**, choose **Add a repository**: `owner/repo`, branch `main`, docs directory `docs/`. Paste the token; it is stored encrypted (this needs `RESEARCH_AGENT_SECRETS_KEY` on the server) and never shown again. You can name an environment variable instead; set it on both the api and the worker.
3. **Save and sync.** A sync starts right away (unless another sync or build is running), then the memory bank builds. **Show docs** lists what was read and which core files each doc informs.

After you change the docs on `main`, choose **Sync now**. **Update memory bank now** re-runs generation without a sync.

## How research uses it

- The research prompt includes a short index of the core files.
- For questions about the project, the agent reads the relevant core file (`read_memory_bank`), then searches the docs (`search_knowledge`) and reads sections (`read_doc`).
- It cites doc sections, never the generated core files.
- Answers attribute doc-based claims ("according to SECURITY.md…"). The docs describe design and intent, which can differ from the code.

Anyone in the workspace can read the core files on the **Memory bank** page.

## What it never does

- **Drafting and the inbox never see the memory bank.** Internal details such as architecture, security, and credentials handling must not end up in public replies.
- **It never writes to the repository.** It reads one branch through the GitHub API; that's all.
