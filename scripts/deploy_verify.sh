#!/usr/bin/env bash
# Verifies a deploy: images on the expected tag, pods ready without restarts,
# migrations applied, API ready, setup state, and no worker errors.
#
# Usage: scripts/deploy_verify.sh <namespace> <tag> <web-url>
#   e.g. scripts/deploy_verify.sh research-agent sha-1234567 https://research.example.com
# Exit codes: 0 healthy, 1 a check failed.
set -euo pipefail
ns="${1:?usage: deploy_verify.sh <namespace> <tag> <web-url>}"
tag="${2:?usage: deploy_verify.sh <namespace> <tag> <web-url>}"
tag="${tag%.yaml}"
url="${3:?usage: deploy_verify.sh <namespace> <tag> <web-url>}"
url="${url%/}"
failed=0
check() { if eval "$2"; then echo "  ok    $1"; else echo "  FAIL  $1"; failed=1; fi; }

echo "== Namespace $ns, tag $tag"
for d in api worker web; do
  img="$(kubectl -n "$ns" get deploy "$d" -o jsonpath='{.spec.template.spec.containers[0].image}')"
  check "$d image is $tag ($img)" "[[ '$img' == *:$tag ]]"
done
# Pods from the previous ReplicaSet can still be Terminating right after a rollout.
not_ready="$(kubectl -n "$ns" get pods --no-headers | awk '$3 != "Running" && $3 != "Completed" && $3 != "Terminating" {print $1" "$3}')"
check "all pods running${not_ready:+ (not: $not_ready)}" "[ -z \"\$not_ready\" ]"
restarts="$(kubectl -n "$ns" get pods --no-headers | awk '$4 != "0" {print $1"="$4}')"
check "no container restarts${restarts:+ ($restarts)}" "[ -z \"\$restarts\" ]"

migrations="$(kubectl -n "$ns" logs deploy/api -c migrate 2>/dev/null | grep -oE '"source":"[^"]+"' | sed -E 's/.*"([^"]+)"$/\1/' | tr '\n' ' ' || true)"
up_to_date="$(kubectl -n "$ns" logs deploy/api -c migrate 2>/dev/null | grep -c 'migrations up to date' || true)"
check "migrations ran (applied this rollout: ${migrations:-none})" "[ '$up_to_date' -ge 1 ]"

ready="$(curl -s -m 10 "$url/api/v1/ready" || true)"
check "API ready via the web service ($ready)" "[[ '$ready' == *ready* ]]"
setup="$(curl -s -m 10 "$url/api/v1/setup/status" || true)"
check "setup status answers ($setup)" "[[ '$setup' == *needs_setup* ]]"
[[ "$setup" == *'"needs_setup":true'* ]] && echo "  note  no users yet: first-run setup is open; the setup token is in: kubectl -n $ns logs deploy/api -c api | grep setup_token"

werr="$(kubectl -n "$ns" logs deploy/worker 2>/dev/null | grep -c '"level":"ERROR"' || true)"
check "no worker errors since start ($werr)" "[ '$werr' -eq 0 ]"
aerr="$(kubectl -n "$ns" logs deploy/api -c api 2>/dev/null | grep -c '"level":"ERROR"' || true)"
check "no api errors since start ($aerr)" "[ '$aerr' -eq 0 ]"

if [ "$failed" -ne 0 ]; then echo "VERIFY FAILED (see docs/runbook.md, Troubleshooting and Rollback)"; exit 1; fi
echo "VERIFIED"
