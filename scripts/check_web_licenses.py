"""Reads `pnpm licenses list --json` on stdin; fails on licenses not allowed.

Every allowed license combines with AGPL-3.0-only. LGPL-3.0 is libvips (via
sharp); CC-BY-4.0 is the caniuse-lite data set; Python-2.0 is argparse.
"""
import json
import sys

ALLOWED = {
    "MIT", "ISC", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "0BSD", "BlueOak-1.0.0",
    "CC0-1.0", "CC-BY-4.0", "Python-2.0", "Unlicense", "LGPL-3.0-or-later", "LGPL-3.0-only",
}


def ok(expr: str) -> bool:
    """SPDX expressions: "A OR B" needs one allowed, "A AND B" needs all."""
    expr = expr.strip("() ")
    if " OR " in expr:
        return any(ok(p) for p in expr.split(" OR "))
    if " AND " in expr:
        return all(ok(p) for p in expr.split(" AND "))
    return expr in ALLOWED


data = json.load(sys.stdin)
if "error" in data:  # pnpm reports failures as {"error": {"code", "message"}}
    print(f"  pnpm licenses failed: {data['error'].get('message', data['error'])}")
    sys.exit(1)
bad = {k: sorted(p["name"] for p in v) for k, v in data.items() if not ok(k)}
for lic, names in bad.items():
    print(f"  not allowed: {lic}: {', '.join(names)}")
sys.exit(1 if bad else 0)
