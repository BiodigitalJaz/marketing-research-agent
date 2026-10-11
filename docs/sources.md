# Monitors and sources

**Sources** say where content comes from. **Monitors** say when to collect, from which sources, and which inbox lanes the results can land in. Everything is read-only: the app reads public feeds and APIs and never posts anywhere.

Admins add and edit sources; editors manage monitors.

## Sources

| Kind | Reads | Notes |
|---|---|---|
| **Google Alert (RSS)** | A Google Alert delivered as an RSS feed | Create the alert at google.com/alerts with *Deliver to: RSS feed* and paste the feed link. One phrase per alert. Google's redirect links are unwrapped. To follow a community without an API, such as Reddit, add `site:reddit.com` to the phrase. |
| **RSS / Atom feed** | Any feed: Stack Exchange tags, blogs, forums, newsletters. Check that a feed is still active: many Stack Exchange tags now get only a few questions a month. | **Keep every item** when the feed is already on topic; otherwise items must match the monitor's query or tracked keywords. |
| **Hacker News** | New, Ask, Show, or Top stories through the official API | **Minimum score** filters low-signal stories. |
| **Web search** | The workspace search backend (SearXNG by default; Brave or Google Programmable Search optional) | Runs the monitor's query or cycles through tracked keywords, a few **Queries per run**. **Limit to sites** restricts to hostnames. Paid backends respect a monthly budget (Settings). |

Common options:

- **Fetch the full article** (feeds) or **Fetch result pages for full text** (search) reads the linked page when only a snippet is available. Fetching respects robots.txt and per-host rate limits.
- **Test** shows what a source would collect right now, without saving anything.
- Each source shows its **health** (ok, degraded, failing) and the last error.

### Community rules

Each source can carry rules that drafts follow for items from it:

- **No links:** links and domain names are removed from drafts.
- **No self-promotion:** drafts that mention the brand are flagged.
- **Affiliation must be disclosed:** a brand mention without a disclosure is marked "do not post as written".
- **Other rules:** free-text notes the draft model reads, such as a community's posting guidelines.

## Monitors

A monitor has:

- **Sources** to collect from;
- an optional **Query** (`"exact phrase"`, `OR` or `|`, `-exclude`); without one, the workspace's tracked keywords are used, and negative keywords always apply;
- **Lanes** its items may land in;
- a **Schedule** (cron expression, every 30 minutes by default).

**Run now** collects immediately. A progress bar under each monitor shows the current or latest run: queued, collecting (sources done), scoring (items scored, opportunities found so far), then a summary. Scoring is usually the slow part, since every item goes through the classify and score models. Each run looks back to the previous run (seven days on the first run), skips items it has already seen, and queues new ones for scoring. A feed whose newest posts are older than that contributes nothing on its first run.

The starter monitor (Hacker News and web search) is created **disabled**: enable it once your brand and keywords are set.

## Collected items

**Collected items** lists everything collected, with each item's outcome: became an opportunity, or why it was filtered (negative keyword, too old, too short, duplicate, not relevant, below the minimum score).

- **Score pending** processes items still waiting.
- **Re-score recent** re-runs the last 14 days of items that produced no opportunity, useful after changing models, keywords, or targeting.

Collected items are kept for `retention.raw_items_days` (90 by default), except items behind an opportunity someone saved, snoozed, or acted on and items a report cites. They stay searchable from Research until then.
