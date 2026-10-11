#!/usr/bin/env bash
# Applies a plan written by scripts/deploy_plan.sh and waits for every
# Deployment in it to roll out. It applies the reviewed file as-is; it never
# renders again.
#
# Usage: scripts/deploy_apply.sh .deploy/plan-<namespace>-<tag>.yaml
# Exit codes: 0 rolled out, 1 apply or rollout failed (see docs/runbook.md, Rollback).
set -euo pipefail
cd "$(dirname "$0")/.."
plan="${1:?usage: deploy_apply.sh <plan-file>}"
[ -f "$plan" ] || { echo "no plan file $plan; run scripts/deploy_plan.sh first" >&2; exit 1; }
ns="$(grep -E '^  namespace: ' "$plan" | awk '{print $2}' | sort -u)"
[ "$(wc -l <<<"$ns")" -eq 1 ] || { echo "plan must target one namespace" >&2; exit 1; }

echo "== Applying $plan to namespace $ns (context $(kubectl config current-context))"
kubectl apply -f "$plan" 2>&1 | grep -v '^Warning'
for d in $(kubectl -n "$ns" get deploy -o name); do
  kubectl -n "$ns" rollout status "$d" --timeout=420s || { echo "ROLLOUT FAILED: $d (see docs/runbook.md, Rollback)" >&2; exit 1; }
done
tag="$(basename "$plan" .yaml)"; tag="${tag#plan-"$ns"-}"
echo "APPLIED. Next: scripts/deploy_verify.sh $ns $tag <web-url>"
