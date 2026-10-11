# The Brand page

Everything the agent knows about your brand. Classification, scoring, drafting, and research all read it, so the more specific it is, the better the inbox and the drafts. Editors and admins can edit it.

**Import** and **Export** (top right) load a brand pack, one YAML or JSON file describing all of this, or download the current brand as one; see [brand-packs.md](brand-packs.md).

## Profile

| Field | Used for |
|---|---|
| **Services** | What you offer. Relevance and fit scoring, drafts. |
| **Value propositions**, **Differentiators** | Why people choose you. Drafts, research. |
| **Voice**, **Voice examples** | How drafts should sound. |
| **Do** | Habits drafts follow, such as "answer the question first". |
| **Don't (banned claims and phrases)** | Claims you never make and words you never use. Entries of three words or fewer, or entries in double quotes, are matched literally: a draft containing one is blocked. Longer entries are guidance that a model checks every draft against. Scoring also flags conversations where a natural reply would need one of these claims ("Reply may need a claim we don't make"). |

### Targeting

| Field | Effect |
|---|---|
| **Prioritize** | Audiences and situations scored up |
| **Avoid** | Audiences and situations scored down and flagged "Avoided audience" |
| **Regions served** | Items clearly outside them are flagged "Outside target region" |
| **Venues we don't post in** | Communities you read but never reply in. Items from them never become inbound opportunities and are flagged "We don't post here"; they can still be thought-leadership material. |
| **Themes to own** | Topics for the thought-leadership lane |
| **Valuable media opportunities** | Outlets and opportunity types worth pitching (media lane) |

## Portfolio

Content you can point people to: case studies, blog posts, work samples, talks. Drafts pick the most relevant item for a conversation by comparing its **Summary** with the post, so write the summary as what the piece covers and who it helps. Add the **URL** once it's published; until then drafts can mention the piece but won't link it.

## ICPs

Ideal customer profiles: industries, company sizes, roles, pain points, and buying signals. They feed relevance scoring (by meaning, through embeddings) and research.

## Keywords

- **Track** keywords drive collection: monitors without their own query match against them, and web-search sources cycle through them a few per run. A keyword can be limited to one lane.
- **Negative** keywords drop items that contain them in the title or opening text (first 600 characters), before any model sees them. A passing mention deeper in an article doesn't count.

## Competitors

Names and domains. Scoring flags "Competitor mentioned", and research uses them for comparisons and teardown reports.

## After you change it

Scoring uses the brand as it is when an item is processed. To re-score recent items that produced nothing, use **Monitors & sources → Collected items → Re-score recent**.
