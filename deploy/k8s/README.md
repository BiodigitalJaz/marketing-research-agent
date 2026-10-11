# Kubernetes manifests

Kustomize layout:

- `base/`: Deployments and Services for api (with the migrate init container), worker, web, and SearXNG; the config ConfigMap; public images.
- `overlays/example-nodeport/`: exposes the web UI on a NodePort.
- `overlays/example-ingress/`: exposes it through an Ingress with TLS.
- `jobs/seed.yaml`: optional headless seed Job.

Requirements, secrets, PostgreSQL, and network egress are in [docs/deployment.md](../../docs/deployment.md).

## Your own cluster

Keep your overlay outside this repo (or in a private repo) so your config, registry, and secrets references never land here. An overlay builds on `base/` and typically sets:

- `images:` your registry and tags (match on the base's image names, `ghcr.io/biodigitaljaz/marketing-research-agent-*`);
- `configMapGenerator:` `research-agent-config` with `behavior: replace` and your `config.yaml`;
- the web Service type or an Ingress, and `imagePullSecrets` for a private registry.

Point the Makefile at it once, in a gitignored `.deploy.env` at the repo root:

```make
K8S_OVERLAY = /path/to/your-deploy/overlay
SEED_JOB = /path/to/your-deploy/seed-job.yaml
DEPLOY_NAMESPACE = research-agent
DEPLOY_URL = https://research.example.com
```

## Deploy

Use the [runbook](../../docs/runbook.md):

```sh
make deploy-plan TAG=sha-1234567     # render, check images, server dry run, kubectl diff (changes nothing)
make deploy TAG=sha-1234567          # apply exactly that plan, wait for rollout
make deploy-verify TAG=sha-1234567   # images, pods, migrations, health, worker errors
make seed-cluster TAG=sha-1234567    # optional idempotent seed Job
```

Images are pinned to commit tags at plan time, so every rollout pulls an exact build. CI builds images on every push to `main` as `ghcr.io/<owner>/<repo>-{api,worker,migrate,web}`, tagged `main` and `sha-<commit>`.
