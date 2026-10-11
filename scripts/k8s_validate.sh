#!/usr/bin/env bash
# Renders a kustomize overlay and schema-validates it. Fails if rendering
# fails or produces no resources (a bare pipe would hide both).
set -euo pipefail
overlay="${1:?usage: k8s_validate.sh <overlay-dir>}"
rendered="$(kubectl kustomize "$overlay")"
count="$(grep -c '^kind:' <<<"$rendered" || true)"
if [ "${count:-0}" -eq 0 ]; then
  echo "k8s validate: $overlay rendered no resources" >&2
  exit 1
fi
kubeconform -strict -summary -kubernetes-version 1.33.0 - <<<"$rendered"
