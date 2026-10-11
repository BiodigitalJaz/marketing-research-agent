# Brand packs

Everything the agent knows about your brand lives on the **Brand** page: what you offer, your tone, who you're for and who you avoid, the claims you never make, keywords, competitors, ideal customer profiles (ICPs), and portfolio pieces. Scoring, drafting, and research all read it.

A **brand pack** is the same information in one YAML (or JSON) file. Use one to set up a new install in one step, to keep your brand definition in version control, or to try the app with an example.

## Importing a pack

On the **Brand** page, **Import** takes a pack file or pasted YAML/JSON and shows what it will change before applying it:

- **Add to the current brand** fills empty profile fields and adds keywords, competitors, ICPs, and portfolio items that aren't there yet (matched by term, name, or title). Nothing you have is changed.
- **Replace the brand** swaps the profile, keywords, competitors, ICPs, and portfolio for the pack's. Opportunities and collected items are kept.

Admins also get **Also rename the workspace** when the pack's `name` differs from the workspace's: prompts call your brand by the workspace name, so research and drafts use it.

**Export** downloads the current brand as a pack: a backup, or a way to move a brand to another install.

From the command line (for example on a fresh, headless install):

```sh
# Docker Compose
docker compose run --rm -v "$PWD/my-brand.yaml:/brand.yaml:ro" seed -brand-file /brand.yaml
docker compose run --rm seed -brand-example benchwright   # embedded example

# Binary
seed -config config.yaml -brand-file my-brand.yaml
```

The command-line seed imports a pack only where the workspace has nothing yet: the profile if there isn't one, ICPs and portfolio if there are none, and keywords and competitors that aren't already there. Re-running the seed never overwrites what you've edited in the UI. When the seed creates a new workspace, it's named after the pack's `name`.

## Format

A complete example for a fictional company ships with the app: [`benchwright.yaml`](../examples/brand-packs/benchwright.yaml) (open-source LLM evaluation for engineering teams). Its niche is discussed daily on Hacker News, so a new install sees a full inbox quickly. Every section is optional, and unknown fields are rejected so typos don't go unnoticed.

```yaml
name: Your Brand

profile:
  services: []          # what you offer
  value_props: []       # why customers choose you
  differentiators: []   # what sets you apart
  tone:
    voice: ""           # how you sound, in a sentence or two
    do: []              # habits drafts should follow
    dont: []            # claims you never make and words you never use;
                        # drafts are checked against this list
    examples: []        # lines that sound like you
  targeting:
    prioritize: []      # audiences and situations to score up
    avoid: []           # audiences and situations to score down
    geography: []       # regions you serve
    excluded_venues: [] # communities you read but never post in
    topics: []          # themes you want to be known for
    media_targets: []   # outlets and opportunity types worth pitching

keywords:
  track: []             # what monitors and web search look for
  negative: []          # items with these in the title or opening are dropped

competitors:
  - name: ""
    domains: []
    notes: ""

icps:
  - name: ""
    industries: []
    company_sizes: []
    roles: []
    pain_points: []
    buying_signals: []

portfolio:
  - kind: blog_post     # case_study | blog_post | work_sample | talk | other
    title: ""
    url: ""
    summary: ""         # what it covers; used to match it to conversations
    tags:
      industries: []
      services: []
      problems: []
```

## Writing a good pack

- **Be specific in `dont`.** "Promise specific time savings" is checkable; "be careful" isn't. Entries of three words or fewer, or any entry in double quotes, are banned phrases: a draft containing one is blocked. Longer entries are guidance that the draft check judges.
- **Describe audiences in `avoid` the way they'd show up in a post** ("job seekers", "students asking for homework help"). Items from them are scored down and flagged.
- **Keep tracked keywords short and concrete.** Web search rotates through them a few per run; long lists take longer to cycle.
- **Summaries in `portfolio` matter more than titles.** They're what drafting uses to pick a relevant piece to mention.
