#!/usr/bin/env bash
# Fails if a dependency's license is not on the allowlist. Every allowed
# license can be combined with this project's AGPL-3.0-only license.
# Go: go-licenses (github.com/google/go-licenses/v2). Web: pnpm, production
# dependencies only (allowlist in check_web_licenses.py).
set -euo pipefail
cd "$(dirname "$0")/.."

go_allowed="MIT,Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MPL-2.0"
# Not recognized by the classifier; checked by hand:
#   github.com/segmentio/asm: MIT No Attribution (MIT-0)
go_ignore=(--ignore github.com/BiodigitalJaz/marketing-research-agent --ignore github.com/segmentio/asm)

if ! out=$(go-licenses check ./... --allowed_licenses="$go_allowed" "${go_ignore[@]}" 2>&1); then
  grep -v '\.s$' <<<"$out" >&2
  echo "license check FAILED: Go dependencies" >&2
  exit 1
fi
echo "Go dependencies: OK"

if ! (cd web && pnpm licenses list --prod --json) | python3 -I scripts/check_web_licenses.py; then
  echo "license check FAILED: web dependencies" >&2
  exit 1
fi
echo "Web dependencies: OK"
