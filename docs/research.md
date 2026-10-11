# Research and reports

**Research** answers questions about your market, audience, competitors, and the conversations you've collected, with every claim tied to a numbered source. Editors and admins can run research; everyone can read the results.

## Two modes

| Mode | Use it for | What happens |
|---|---|---|
| **Ask a question** (chat) | Quick questions and follow-ups | One agent works through tools and answers in a few minutes. Ask **Follow-up** questions in the same thread. |
| **Deep research** | A written report | The agent plans sub-questions, researches each one, checks its key claims against what the cited sources said, and writes a report from a **Report template**: Freeform, Competitor teardown, Market scan, Pain points, or Topic deep dive. |

**Use the web** lets the agent search and read public web pages. Turn it off to answer only from what your monitors have collected, your brand profile, and internal docs.

While a job runs, its steps stream in live (searches, pages read, notes). **Cancel** stops it. Chat answers carry an "AI answer — check the sources" label; reports carry "AI-generated research — review before sharing".

## Where answers come from

The agent picks from these tools:

| Tool | Reads |
|---|---|
| Collected content | Everything your monitors collected, including items that never became opportunities, by keyword and meaning |
| Brand | Your profile, ICPs, portfolio, and competitors |
| Opportunity stats and charts | Counts and trends from the inbox, drawn as charts from real data only |
| Web search and page reading | Public pages, fetched politely (robots.txt, rate limits) and never from private addresses |
| Internal knowledge | Your product docs and memory bank, when a repository is connected ([memory-bank.md](memory-bank.md)) |
| MCP tools | Read-only tools an admin has connected and allowlisted |

Admins choose the model and limit which tools are available under **Settings → Routes → research**.

## Citations

Every source a tool returns gets a number, and the answer cites those numbers, like [3]. The list under **Sources** shows each one with its link. The agent can only cite sources it actually read during the job: numbers it didn't see are removed.

Deep research also checks its key claims. The agent keeps the text each tool showed it for every source (a search snippet, a collected post, a fetched page). Before writing the report, each claim is compared with the passage of its cited sources that best matches it. The report ends with **Key claims and evidence**, which lists the supported claims and marks those resting on a single source, and **Not supported by the cited sources**, which lists claims the sources don't back, with what is missing. The report is written without presenting unsupported claims as findings, and **How it was researched** shows the same check.

The check is a model's judgment on short excerpts, so it can miss support that sits elsewhere in a long page, or accept a loose match.

Treat answers as a researched starting point, not a verdict. Open the sources behind anything you'll act on.

## Reports

Deep research produces a report under **Reports**: the Markdown text, charts, and citations. **Export Markdown** downloads it. **How it was researched** shows the plan and steps.

## Archiving and deleting

**Archive** hides a finished job from the research list; tick **Show archived** to see archived jobs and **Unarchive** them. **Delete** removes a job with its steps and chat; the person who started it or an admin can delete it, and a running job must be cancelled first. Reports a job produced are kept under **Reports**.

## Limits

| | Chat | Deep |
|---|---|---|
| Agent steps | 14 | 40 |
| Time | 8 minutes | 15 minutes to gather, plus up to 10 minutes to verify and write |

Deep research splits its steps and its gathering time evenly across the sub-questions it planned, so each one is researched even with a slow model; a sub-question that runs out of its share writes notes from what it found. Admins can change max agent steps and the token budget per route, and the deep research time limit (up to 30 minutes); chat always stops at 8 minutes. If the agent repeats an identical tool call, it gets a notice instead of a second result. On long runs, older tool results are summarized as the model's context window fills, so models with small context windows can still finish; more context gives better answers.

## Tips

- Name the audience and the time frame: "What do small agency owners complain about in project tools this month?" beats "project tools".
- For counts ("how many opportunities by lane this week"), ask directly; the agent uses real inbox data and can chart it.
- If an answer says evidence is thin, that's useful: widen sources or keywords under **Monitors & sources** and ask again later.
