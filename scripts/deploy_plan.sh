#!/usr/bin/env bash
# Plans a Kubernetes deploy without changing anything:
#   1. renders the overlay with images pinned to TAG,
#   2. checks the render (one namespace, every app image pinned, images exist),
#   3. validates it (kubeconform) and dry-runs it on the server,
#   4. shows `kubectl diff` against what is running.
# Writes the exact manifests to .deploy/plan-<namespace>-<tag>.yaml; apply
# that file with scripts/deploy_apply.sh (or `make deploy`), never a fresh
# render, so what was reviewed is what gets applied.
#
# Usage: scripts/deploy_plan.sh <overlay-dir> <tag>     (tag like sha-1234567)
# Exit codes: 0 plan ready, 1 a check failed (do not apply).
set -euo pipefail
cd "$(dirname "$0")/.."
overlay="${1:?usage: deploy_plan.sh <overlay-dir> <tag>}"
tag="${2:?usage: deploy_plan.sh <overlay-dir> <tag>}"
fail() { echo "PLAN FAILED: $*" >&2; exit 1; }

echo "== Context: $(kubectl config current-context)"
rendered="$(kubectl kustomize "$overlay")" || fail "kustomize could not render $overlay"
rendered="$(sed -E "s#(image: [^ ]+-(api|worker|migrate|web)):main#\1:${tag}#g" <<<"$rendered")"

namespaces="$(grep -E '^  namespace: ' <<<"$rendered" | awk '{print $2}' | sort -u)"
[ "$(wc -l <<<"$namespaces")" -eq 1 ] && [ -n "$namespaces" ] || fail "the render must target exactly one namespace; found: $(tr '\n' ' ' <<<"$namespaces")"
ns="$namespaces"
echo "== Namespace: $ns"

images="$(grep -oE 'image: [^ ]+-(api|worker|migrate|web):[^ ]+' <<<"$rendered" | awk '{print $2}' | sort -u)"
[ "$(wc -l <<<"$images")" -eq 4 ] || fail "expected 4 app images (api, worker, migrate, web); found: $(tr '\n' ' ' <<<"$images")"
grep -q ":main$" <<<"$images" && fail "an image is still on :main; pinning failed"
echo "== Images:"; sed 's/^/   /' <<<"$images"

if [ "${SKIP_IMAGE_CHECK:-}" = "1" ]; then
  echo "== Image check skipped (SKIP_IMAGE_CHECK=1)"
elif command -v docker >/dev/null; then
  while read -r img; do
    docker manifest inspect "$img" >/dev/null 2>&1 || fail "image $img not found. Was it built (check the images workflow for this commit)? For a private registry, run docker login first, or set SKIP_IMAGE_CHECK=1 if you have verified it another way."
  done <<<"$images"
  echo "== Images exist in the registry"
else
  echo "== WARNING: docker not installed; could not confirm the images exist. Check the images workflow for $tag before applying."
fi

mkdir -p .deploy
plan=".deploy/plan-${ns}-${tag}.yaml"
printf '%s\n' "$rendered" > "$plan"

echo "== Schema validation"
if command -v kubeconform >/dev/null; then
  kubeconform -strict -summary -kubernetes-version 1.33.0 "$plan" || fail "kubeconform rejected the manifests"
else
  echo "   kubeconform not installed; skipped (make tools installs it)"
fi

echo "== Server dry run"
kubectl apply --dry-run=server -f "$plan" 2>&1 | grep -v '^Warning' || fail "the server rejected the manifests"

echo "== Changes against the cluster (kubectl diff)"
set +e
kubectl diff -f "$plan"
rc=$?
set -e
case $rc in
  0) echo "   (no changes)" ;;
  1) ;;
  *) fail "kubectl diff failed (exit $rc)" ;;
esac

echo
echo "PLAN READY: $plan"
echo "Review the diff above, then apply with: scripts/deploy_apply.sh $plan"
