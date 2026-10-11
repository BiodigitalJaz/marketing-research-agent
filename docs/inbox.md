# Using the inbox

The inbox is your team's daily triage queue. This guide explains what lands in it, how to read an item, what each action does, and how to respond.

**Nothing is ever posted for you.** The inbox finds an opening, explains why it matters, and prepares a draft. You decide, copy, and post it yourself on the original site.

## Where items come from

Monitors collect posts, questions, and articles from their sources on a schedule (every 30 minutes for the starter monitor, which starts **disabled**: enable it under **Monitors & sources** once your brand and keywords are set). Sources include RSS feeds (Google Alerts among them), web search, and Hacker News. Each item then goes through the pipeline:

1. **Pre-filter.** Items are dropped if a negative keyword appears in the title or opening text (for example "jobs" or "hiring"), if they're too old for every lane, or if they're too short.
2. **De-duplicate.** The same content seen twice, or a near-identical item, is merged.
3. **Classify.** The model decides which lanes, if any, the item fits.
4. **Score.** Each candidate lane gets a score from relevance, intent, fit, recency, reach, and (for media) deadline. Anything below the lane's minimum score (default 0.5) is dropped.

What's left is an **opportunity**: a specific place where a reply, pitch, or comment could help. Most collected items are filtered out, and that's expected; the inbox is meant to be short.

## How it's organized

### Lanes (the tabs)

Each lane implies a kind of response:

| Lane | What it is | Response | Fresh for |
|---|---|---|---|
| **Inbound** | Someone has a problem or question your brand genuinely helps with | A **reply** | 48 hours |
| **Media & speaking** | A journalist query, podcast guest call, call for speakers, or guest-post invitation | A **pitch** | 14 days (or until its deadline) |
| **Thought leadership** | A discussion or trend worth a substantive point of view | A **comment**, or a topic for an article | 4 days |

Daily quotas, freshness windows, minimum scores, and score weights are editable under **Settings → Lanes** (admins only).

### Today's picks

Each morning after 06:00 in the inbox time zone (`inbox.timezone` in the config, UTC by default), the best new items in each lane are featured, up to that lane's daily quota (inbound 5, media 3, thought leadership 4 by default). Drafts for featured items are prepared ahead of time. Everything else is listed below them, best score first.

### Status filter

The dropdown switches between **New** (to triage), **Saved**, **Snoozed**, **Acted**, **Dismissed**, and **Expired**. An item expires when its lane's freshness window or its deadline passes while it's still new.

## Reading an item

- **Title.** Click it, or press Enter, to open the detail page.
- **The line under the title:** lane, opportunity type (for example "Pain point", "Journalist query"), where it was posted, how long ago, and a deadline badge if one applies.
- **Score** (top right). Hover it to see the factors behind it: relevance, intent (how actively the person is asking), fit, recency, reach, and deadline.
- **Explanation.** One or two sentences on why it surfaced. Read this first; it's the fastest way to judge an item.
- **Flags** from scoring:

| Flag | Meaning |
|---|---|
| **Reply may need a claim we don't make** (red) | A natural reply would drift toward something on the brand's "Claims it never makes" list. Be careful, or pass. |
| **Hype or scam** (red) | The post makes unrealistic promises or looks like a scam or spam. Don't engage directly; it may still be a thought-leadership topic. |
| **We don't post here** | A venue on the brand's excluded-venues list. Use it as a signal or an article idea, not a place to reply. |
| **Avoided audience** | An audience on the brand's "Avoid" list. |
| **Outside target region** | The author is clearly outside the brand's target geography. |
| **Competitor mentioned** | Useful context, especially for comparisons people are asking about. |

## The actions

| Action | When to use it | What it does |
|---|---|---|
| **Open original** (o) | Always, before acting. Check the full thread and whether it's already answered. | Opens the post in a new tab and logs that you looked. |
| **Details** (title, or Enter) | You want to respond. | Opens the detail page with drafts. |
| **Save** (s) | Worth doing, but not right now. | Moves it to *Saved* so it doesn't expire unnoticed. |
| **Snooze 1d** | Promising but early, for example a discussion that needs to develop. | Hides it for a day, then returns it as new. |
| **Mark acted** | You've posted a reply or sent a pitch yourself. | Moves it to *Acted*. This is what response tracking counts. |
| **Dismiss** (d) | Not worth a response. | Asks for a reason (below). |
| **Restore** (detail page) | You changed your mind. | Returns a saved, snoozed, acted, or dismissed item to *New*. |

**Choose dismiss reasons carefully.** The reasons are off topic, low intent, wrong fit, too old, duplicate, bad source, and other. They're stored with the item so scoring can learn which sources, communities, and topics waste your time; using them to adjust scores is planned for a later release. Several "bad source" dismissals on one feed, for example, are a good reason to remove that feed under **Monitors & sources**.

Keyboard shortcuts work on the inbox list: **j/k** move, **o** opens the original, **Enter** opens details, **s** saves, **d** dismisses (press 1–7 to pick a reason).

## Responding: the detail page

The detail page shows the original post, the score breakdown, the drafts, and an outcome log.

1. **Drafts are generated when an editor opens the item**, or are already waiting for today's picks. Generation can take a minute or two on a local model; the page updates on its own. Variants depend on the lane:
   - **Replies:** *helpful* (answers the question first), *story* (brief first-person experience), *short* (two or three sentences).
   - **Pitches:** one, with a subject line and an `[availability]` placeholder for you to fill in.
   - **Comments:** *perspective* and *brief*. Comments carry no links.
2. **Check each draft's checks before copying:**
   - **Do not post as written** (red): a hard problem, such as a banned phrase, a missing affiliation disclosure where the community requires one, or a likely claim the brand doesn't make. Edit it or regenerate.
   - **Check** (amber): worth a look, such as being over the word limit or possibly breaking a community rule.
   - **Note** (grey): information only, such as a link that was removed because the community doesn't allow links.
3. **Regenerate with instruction** when a draft isn't right, for example "Shorter, answer the pricing question only, don't mention the product". You can also switch the draft kind (reply, pitch, comment) there.
4. **Copy the draft**, edit it into your own voice on the original site, and post it yourself. Every draft is labelled "AI draft — review before posting" for a reason. Copying is logged.
5. **Mark acted** on the detail page.
6. **Log the outcome** when you know it: no response, got a reply, moved to DM, meeting booked, became a lead, or became a customer. Outcomes show which lanes, communities, and draft styles actually produce results.

Community rules shape the drafts. Each source can carry rules (no links, no self-promotion, affiliation must be disclosed, plus free-text notes), edited under **Monitors & sources**. Links a community forbids are removed from drafts automatically, whatever the model writes.

## A daily routine (10–15 minutes)

1. Open the inbox and start with **Today's picks**.
2. For each item, read the explanation and open the original. Then:
   - **respond**: details, copy, post, Mark acted;
   - **save** it for later; or
   - **dismiss** it with a reason.
3. Skim the rest of **New**, then check **Saved** for anything with a deadline coming up.
4. Once a week, filter by **Acted** and log any outcomes you've learned since.

## Rules for anything you post

Your brand's rules (the tone and "Claims it never makes" lists on the **Brand** page) apply to replies as much as to marketing pages. Drafts are checked against them, but you post, so you check too:

- **Answer the person's question first.** Mention your product only when it genuinely helps.
- **When you mention your product, disclose that you work on it** in the same message. Most communities require it.
- **Never make a claim on the brand's "never" list.** Replies are where this is easiest to slip.

## When the inbox is quiet

A short inbox usually means few new conversations, not a fault. To widen what comes in:

- **Add sources** under **Monitors & sources** (admins): Google Alerts delivered as RSS, any RSS or Atom feed (Stack Exchange tags, blogs, forums), or web-search sources. Each source needs to be attached to a monitor. To follow a community without an API, such as Reddit, create a Google Alert for `site:reddit.com "your phrase"`.
- **Test a source** to see what it would collect right now, without saving anything, and **Run now** on a monitor to collect immediately.
- **Review tracked and negative keywords** under **Brand**. Web search cycles through every tracked keyword, a few per run. Negative keywords drop an item when they appear in its title or opening text.
- **Re-score recent items** after changing models, keywords, or targeting: **Monitors & sources → Collected items → Re-score recent**. This re-runs the last 14 days of items that produced no opportunity.

Everything collected, including items that didn't become opportunities, stays searchable from **Research**.
