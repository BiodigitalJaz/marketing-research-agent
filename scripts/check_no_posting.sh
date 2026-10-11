#!/usr/bin/env bash
# Fails if connector or agent-tool code contains an HTTP write method.
#
# The system must never post, comment, message, or submit anything to an
# external platform. Connectors and agent tools are GET-only, with no
# exceptions.
#
# Usage: scripts/check_no_posting.sh [dir ...]   (defaults below)
set -euo pipefail

cd "$(dirname "$0")/.."

dirs=("$@")
if [ ${#dirs[@]} -eq 0 ]; then
  dirs=(internal/sources internal/fetch internal/search internal/collect internal/agent internal/research internal/knowledge)
fi

existing=()
for d in "${dirs[@]}"; do
  [ -d "$d" ] && existing+=("$d")
done
if [ ${#existing[@]} -eq 0 ]; then
  echo "no-posting check: no connector directories yet; OK"
  exit 0
fi

# Write verbs as method constants, string literals, or stdlib/client helpers.
pattern='http\.Method(Post|Put|Patch|Delete)|"(POST|PUT|PATCH|DELETE)"|http\.(Post|PostForm)\(|\.(Post|PostForm|Put|Patch|Delete)\(|NewRequest(WithContext)?\([^,]*,[[:space:]]*"(POST|PUT|PATCH|DELETE)"'

violations=$(grep -RInE --include='*.go' "$pattern" "${existing[@]}" \
  | grep -v '_test\.go:' || true)

if [ -n "$violations" ]; then
  echo "no-posting check FAILED: HTTP write methods found in connector/agent code:"
  echo "$violations"
  echo
  echo "Connectors must be read-only. See CLAUDE.md 'Hard rules'."
  exit 1
fi
echo "no-posting check: OK (${existing[*]})"
