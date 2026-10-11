# Deployment runbook

Step-by-step procedures for installing, upgrading, and rolling back Marketing Research Agent on Kubernetes (and, briefly, Docker Compose). They are written to be followed by a person or by an AI agent: every step says what to run, what you should see, and when to stop.

Background and reference material live elsewhere: requirements and architecture of a deployment in [deployment.md](deployment.md), every setting in [configuration.md](configuration.md), first-run setup in [getting-started.md](getting-started.md).

Placeholders look like `<namespace>`. Commands assume a shell at the repository root with `kubectl` pointed at the right cluster.

## Ground rules

These are mandatory for agents and strongly recommended for people.

1. **Approval gates.** Anything that changes a running environment needs explicit approval from the owner for that specific action: `kubectl apply`/`delete`, `make deploy`, database writes or restores, and secret changes. Read-only steps never need approval: rendering, `make deploy-plan` (server dry run and diff), `make deploy-verify`, logs, and read-only SQL. Approval for one environment or one change does not carry over to another.
2. **Apply only what was reviewed.** `make deploy` applies the exact file `make deploy-plan` wrote. Never re-render between plan and apply, and never apply hand-edited manifests.
3. **Only deploy builds that passed.** The commit's CI run and images workflow must both have succeeded. A merge whose workflows didn't run has no images.
4. **Back up before migrations.** If the change adds files under `migrations/`, back up the database first and check the backup.
5. **Secrets stay secret.** Never print, log, paste, or commit secret values (database passwords, `RESEARCH_AGENT_SECRETS_KEY`, API keys, setup tokens in shared channels). Never put a password on a command line: other users can see it in the process list. Pass it on stdin or in a file readable only by you, and delete the file afterwards.
6. **One namespace per plan.** Check the namespace the plan targets before applying. A plan that touches anything else is wrong.
7. **Stop on failure.** If a check fails, stop and report what failed with its output. Don't improvise fixes against a production environment; the [troubleshooting](#troubleshooting) table and [rollback](#rollback) cover the known cases.

Each step below uses this format:

- **Do:** the command or action.
- **Expect:** what success looks like.
- **Stop if:** conditions that mean you must not continue.

## Tools

| Tool | Used for |
|---|---|
| `kubectl` (with kustomize) | Render, diff, apply, logs |
| `make`, `bash`, `curl` | The deploy scripts |
| `docker` | Checking images exist in the registry (`docker login` for a private registry) |
| `kubeconform` | Schema validation (`make tools` installs it) |
| `gh` (optional) | Checking CI and image workflow runs |
| `openssl` | Generating secrets |

## The deploy scripts

| Command | Script | What it checks | Changes anything? |
|---|---|---|---|
| `make deploy-plan TAG=<tag>` | `scripts/deploy_plan.sh` | Renders `K8S_OVERLAY` with images pinned to `<tag>`; exactly one namespace; all four app images pinned and present in the registry; kubeconform; server-side dry run; `kubectl diff`. Writes `.deploy/plan-<namespace>-<tag>.yaml`. | No |
| `make deploy TAG=<tag>` | `scripts/deploy_apply.sh` | Applies that plan file (refuses if there isn't one) and waits for every Deployment to roll out. | **Yes** |
| `make deploy-verify TAG=<tag>` | `scripts/deploy_verify.sh` | Images on `<tag>`; pods running with no restarts; migrations ran; API ready through the web service; setup status; no errors in the api or worker logs. | No |
| `make seed-cluster TAG=<tag>` | — | Optional headless seed Job in `DEPLOY_NAMESPACE`. | **Yes** |

All exit non-zero on failure and print `PLAN FAILED`, `ROLLOUT FAILED`, or `VERIFY FAILED` with the reason.

They read a gitignored `.deploy.env` at the repository root:

```make
K8S_OVERLAY = /path/to/your-deploy/overlay       # your overlay, kept outside this repo
SEED_JOB = /path/to/your-deploy/seed-job.yaml    # optional; copy deploy/k8s/jobs/seed.yaml and set its image to your registry
DEPLOY_NAMESPACE = research-agent
DEPLOY_URL = https://research.example.com        # the web address users open
```

## Fresh install

### 1. Database

**Do:** as a PostgreSQL superuser, create a role and database for the app and the four extensions. Generate the password into a file (`openssl rand -hex 24 > db.pw; chmod 600 db.pw`) and paste it in interactively rather than putting it on a command line.

```sql
CREATE ROLE research_agent LOGIN PASSWORD '<generated>';
CREATE DATABASE research_agent OWNER research_agent;
\c research_agent
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
SELECT extname, extversion FROM pg_extension ORDER BY 1;
```

**Expect:** `citext`, `pg_trgm`, `pgcrypto`, `vector` (plus `plpgsql`).
**Stop if:** an extension is unavailable (`pg_available_extensions`); install pgvector on the server first.

### 2. Namespace and secrets

**Do:** write the values to a file only you can read, create the secrets from files, then delete them. Values never appear on a command line.

```sh
kubectl create namespace <namespace>
umask 077

# App secret (one KEY=value per line; read the database password from db.pw).
{
  printf 'RESEARCH_AGENT_DATABASE_URL=postgres://research_agent:%s@<db-host>:5432/research_agent?sslmode=require\n' "$(cat db.pw)"
  printf 'RESEARCH_AGENT_SECRETS_KEY=%s\n' "$(openssl rand -base64 32)"
  printf 'RESEARCH_AGENT_SERVER_BASE_URL=%s\n' '<web-url>'
  printf 'LOCAL_LLM_BASE_URL=%s\n' '<openai-compatible-url, optional>'
  # Optional: ANTHROPIC_API_KEY=..., OPENAI_API_KEY=..., BRAVE_API_KEY=... (or enter keys in Settings later)
} > app.env
kubectl -n <namespace> create secret generic research-agent-env --from-env-file=app.env

printf 'SEARXNG_SECRET=%s\n' "$(openssl rand -hex 32)" > searxng.env
kubectl -n <namespace> create secret generic searxng-secret --from-env-file=searxng.env

# Only for a private registry: a registry token in token.txt.
printf '{"auths":{"ghcr.io":{"auth":"%s"}}}' "$(printf '%s:%s' '<user>' "$(cat token.txt)" | base64 -w0)" > dockercfg.json
kubectl -n <namespace> create secret generic ghcr-secret --type=kubernetes.io/dockerconfigjson --from-file=.dockerconfigjson=dockercfg.json

rm -f app.env searxng.env dockercfg.json token.txt db.pw
```

(`printf` is a shell builtin, so its arguments don't show up in the process list the way an external command's do.)

Back up `RESEARCH_AGENT_SECRETS_KEY` somewhere safe (a password manager): without it, keys entered in Settings can't be decrypted.

**Expect:** `kubectl -n <namespace> get secrets` lists the secrets (never print their values).
**Stop if:** the namespace already contains another installation.

### 3. Overlay

**Do:** copy `deploy/k8s/overlays/example-ingress` (or `example-nodeport`) to a directory outside this repository and set:

- `namespace: <namespace>`, plus a patch renaming the `Namespace` object if it isn't `research-agent`;
- `images:` matching the base's image names (`ghcr.io/biodigitaljaz/marketing-research-agent-*`) with your registry and `newTag: main`;
- the Ingress host and TLS secret, or a NodePort (pin `nodePort` to a free port so the URL is known);
- `imagePullSecrets` for a private registry;
- optionally `configMapGenerator: research-agent-config` with `behavior: replace` and your own `config.yaml`.

Then point `.deploy.env` at it.

**Expect:** `make k8s-validate` passes.

### 4. Plan

**Do:** `make deploy-plan TAG=<tag>` with the `sha-` tag of the build you're installing.
**Expect:** `PLAN READY`; the diff creates resources only in `<namespace>`.
**Stop if:** `PLAN FAILED`, or the diff touches another namespace.

### 5. Apply (approval required)

**Do:** `make deploy TAG=<tag>`.
**Expect:** every Deployment reports `successfully rolled out`.
**Stop if:** `ROLLOUT FAILED`; see [Troubleshooting](#troubleshooting).

### 6. Verify

**Do:** `make deploy-verify TAG=<tag>`.
**Expect:** `VERIFIED`, with a note that first-run setup is open.

### 7. First-run setup

**Do:**
1. Get the token: `kubectl -n <namespace> logs deploy/api -c api | grep setup_token`. Treat it as a secret.
2. Open `<web-url>`; it redirects to `/setup`. Enter the token, workspace name, and admin account.
3. Work through **Getting started** ([getting-started.md](getting-started.md)). For the model, routes must name models your server actually serves, and thinking models need thinking turned off in each route's extra fields (`reasoning_effort: none` for many OpenAI-compatible servers) or calls hit the five-minute timeout.

**Expect:** the checklist reaches 6 of 6.

### 8. Smoke test

Do each and confirm:

1. **Settings → Playground:** a message to the routed model answers.
2. **Brand:** import a [brand pack](brand-packs.md) or fill in the profile; Export downloads it back.
3. **Monitors & sources:** add a feed, **Test** it, enable a monitor, **Run now**; the progress bar goes queued → collecting → scoring → summary.
4. **Collected items** show filter reasons; relevant items become opportunities in the **Inbox**.
5. **Drafts** generate on an opportunity, labelled as AI drafts with rule checks.
6. **Research:** a chat question answers with numbered citations.
7. **Settings → Workspace & users:** add a viewer; they can sign in and can't open Settings.

## Upgrade

### 1. Pick and check the build

**Do:**

```sh
git fetch origin && git log --oneline -1 origin/main     # the commit to deploy
TAG=sha-$(git rev-parse --short=7 origin/main)
gh run list --branch main --limit 4                       # CI and Images for that commit
kubectl -n <namespace> get deploy api -o jsonpath='{.spec.template.spec.containers[0].image}'   # what's running
```

**Expect:** CI and Images both `success` for that commit.
**Stop if:** either failed or didn't run. Fix and re-run, or deploy the newest commit whose workflows passed.

### 2. Read what changes

**Do:**

```sh
DEPLOYED=<sha from the running image>
git log --oneline "$DEPLOYED"..origin/main
git diff --name-only "$DEPLOYED"..origin/main -- migrations/
```

**Expect:** you know which features ship and whether there are new migrations.
**Stop if:** a migration looks destructive (drops tables or columns with data) and no backup plan is agreed.

### 3. Back up the database (required when there are new migrations)

**Do:** dump with `pg_dump -Fc`, passing the password on stdin. For PostgreSQL running in the cluster:

```sh
mkdir -p <backup-dir> && chmod 700 <backup-dir>
F=<backup-dir>/<db>-$(date -u +%Y%m%dT%H%M%SZ)-pre-$TAG.dump
kubectl -n <pg-namespace> get secret <pg-secret> -o jsonpath='{.data.<password-key>}' | base64 -d \
  | kubectl -n <pg-namespace> exec -i <pg-pod> -- sh -c 'IFS= read -r PGPASSWORD; export PGPASSWORD; pg_dump -U postgres -Fc <db>' > "$F"
chmod 600 "$F"
head -c 5 "$F"; echo                                     # expect PGDMP
kubectl -n <pg-namespace> cp "$F" <pg-pod>:/tmp/verify.dump
kubectl -n <pg-namespace> exec <pg-pod> -- sh -c 'pg_restore --list /tmp/verify.dump | grep -c "TABLE DATA"; rm -f /tmp/verify.dump'
```

(`pg_restore` can't read a dump piped through `kubectl exec` on stdin, which is why the file is copied in.)

**Expect:** the file starts with `PGDMP`, and the table-data count roughly matches the number of tables (40+).
**Stop if:** the dump is empty or the header is wrong.

### 4. Plan and review

**Do:** `make deploy-plan TAG=$TAG`, then read the diff.

**Expect, typically:** new image tags on the api, worker, and web Deployments (and the migrate init container); a new `research-agent-config-<hash>` ConfigMap only if the config changed; SearXNG only if its settings changed.

**Stop if:** the diff deletes a Service, the Namespace, or a PersistentVolumeClaim, or changes anything outside `<namespace>`.

### 5. Apply (approval required)

**Do:** `make deploy TAG=$TAG`.
**Expect:** all Deployments roll out. Migrations run in the api pod's init container before the api starts; the worker waits for them.
**Stop if:** `ROLLOUT FAILED`; go to [Rollback](#rollback).

### 6. Verify

**Do:** `make deploy-verify TAG=$TAG`, then a short smoke test (Playground message, one monitor run, one research question).
**Expect:** `VERIFIED`. An expected warning in the api log: unset API keys for providers you don't use.

## Rollback

Choose the smallest step that fixes the problem, and get approval for it.

### Application rollback (no restore)

When the change had no new migrations, or the migrations only added tables or columns:

1. `make deploy-plan TAG=<previous-tag>`: review the diff (images back to the previous tag).
2. `make deploy TAG=<previous-tag>`
3. `make deploy-verify TAG=<previous-tag>`

Migrations are not reversed automatically. The previous build usually runs fine against added tables and columns; if it fails at startup on the newer schema, restore the database.

### Database restore

Data written after the backup is lost, so this is a last resort.

1. Stop writers: `kubectl -n <namespace> scale deploy/api deploy/worker --replicas=0`.
2. Restore into the app's database, passing the password on stdin as in the backup step:
   `pg_restore -U postgres --clean --if-exists --no-owner --role=<app-role> -d <db> /tmp/<backup>.dump` (copy the dump into the PostgreSQL pod first with `kubectl cp`).
3. Plan and deploy the tag that matches the backup (`pre-<tag>` in its file name is the build that was running before), then scale back up: `kubectl -n <namespace> scale deploy/api deploy/worker --replicas=1`.
4. `make deploy-verify TAG=<that-tag>`.

## Docker Compose

| Task | Commands |
|---|---|
| Install | `cp .env.example .env` (set `RESEARCH_AGENT_SECRETS_KEY`), `make dev`, `docker compose logs api \| grep setup_token`, open http://localhost:3000 |
| Back up | `docker compose exec -T postgres pg_dump -U research -Fc research_agent > backup.dump` |
| Upgrade | Back up, `git pull`, `make dev` (rebuilds and restarts; migrations run first), then `curl -s localhost:3000/api/v1/ready` |
| Roll back | `git checkout <previous-commit>`, `make dev`; restore with `docker compose exec -T postgres pg_restore -U research --clean --if-exists -d research_agent < backup.dump` |

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `PLAN FAILED: image … not found` | The images workflow didn't run or failed for that commit (or no `docker login` for a private registry) | Check the commit's workflow runs; fix and re-run, or deploy the last commit that built |
| Pod stuck in `ImagePullBackOff` | Wrong image name or tag, or missing pull secret | Compare the image with the plan; check `imagePullSecrets` and the secret |
| `ImagePullBackOff` with `403 Forbidden` from the registry | The pull secret's token can't read these packages (for example, a token scoped to another project) | Create the pull secret from a token with read access to these images |
| api init container `migrate` fails | Database unreachable, missing extension, or a migration error | `kubectl -n <namespace> logs deploy/api -c migrate`; fix the cause; roll back if needed |
| Worker logs "waiting for database migrations" for minutes | The api (which runs migrations) isn't starting | Check the api pod and its migrate init container |
| The setup log shows `http://localhost:3000/setup` | `server.base_url` isn't set | Set `RESEARCH_AGENT_SERVER_BASE_URL` in the secret; restart the api |
| Model calls time out after about five minutes | A thinking model with thinking on, or a route naming a model the server doesn't serve | Set `reasoning_effort: none` (or the server's equivalent) in each route's extra fields; point routes at models listed by **Discover on <provider>** (Settings → Models) |
| "Connect a language model" stays open | A route's models haven't answered yet | Send a Playground message to each routed model |
| Embeddings step stays open | Nothing embedded yet, or the embeddings server is unreachable | Check the worker log for embed errors; **Detect** in Settings tests the model |
| Monitors collect but the inbox stays empty | Items aren't relevant to the brand (often news feeds), feeds are inactive (no posts within the first-run window), or new Google Alerts haven't filled yet | **Collected items** shows filter reasons; check feeds have recent posts; add active sources (Hacker News for developer topics, `site:reddit.com` alerts); wait a day for new alerts |
| The local model server is unreachable from the cluster | The server isn't running or listens only on localhost | `kubectl run nettest --rm -i --restart=Never --image=curlimages/curl -- curl -s -m 5 <url>/models` |
| `kubectl apply` reports a NodePort conflict | The pinned `nodePort` is in use | List used ports with `kubectl get svc -A`, pick a free one |
| A setting in the secret seems ignored | Variables must use the `RESEARCH_AGENT_` prefix, and nested keys a double underscore | The api log lists prefixed variables that match no setting; fix the names and restart api and worker |
