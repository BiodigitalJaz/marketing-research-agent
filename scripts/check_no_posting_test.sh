#!/usr/bin/env bash
# Self-test for check_no_posting.sh: a GET passes; a POST fails, even with a
# comment claiming an exception.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d "${TMPDIR:-/tmp}/nopost.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
rel=$(realpath --relative-to=. "$tmp")

printf 'package x\nvar _ = "GET"\n' > "$tmp/ok.go"
scripts/check_no_posting.sh "$rel" >/dev/null || { echo "FAIL: GET flagged"; exit 1; }

printf 'package x\nfunc f() { http.NewRequestWithContext(ctx, http.MethodPost, u, nil) }\n' > "$tmp/bad.go"
if scripts/check_no_posting.sh "$rel" >/dev/null; then echo "FAIL: POST not flagged"; exit 1; fi
rm "$tmp/bad.go"

printf 'package x\nfunc f() { c.PostForm(tokenURL, v) // no-posting:allow oauth-token-exchange\n}\n' > "$tmp/oauth.go"
if scripts/check_no_posting.sh "$rel" >/dev/null; then echo "FAIL: annotated POST not flagged"; exit 1; fi

echo "check_no_posting self-test: OK"
