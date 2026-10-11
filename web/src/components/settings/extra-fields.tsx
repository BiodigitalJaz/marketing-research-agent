"use client";

import { useState } from "react";

import { Textarea } from "@/components/ui/textarea";

import { Row } from "./common";

/**
 * parseExtra reads provider-specific request fields: a JSON object, or one
 * `key: value` per line. Line values that parse as JSON (numbers, true/false,
 * null, objects, quoted strings) keep their type; anything else is a string.
 */
export function parseExtra(text: string): Record<string, unknown> {
  const t = text.trim();
  if (t.startsWith("{")) {
    const v = JSON.parse(t);
    if (typeof v !== "object" || v === null || Array.isArray(v)) throw new Error("must be a JSON object");
    return v as Record<string, unknown>;
  }
  const out: Record<string, unknown> = {};
  t.split("\n").forEach((line, i) => {
    const l = line.trim();
    if (!l || l.startsWith("#")) return;
    const m = /^([A-Za-z0-9_.-]+)\s*[:=]\s*(.*)$/.exec(l);
    if (!m) throw new Error(`line ${i + 1}: expected key: value`);
    const raw = m[2].trim();
    let v: unknown = raw;
    try {
      v = JSON.parse(raw);
    } catch {
      // a bare word such as none or high stays a string
    }
    out[m[1]] = v;
  });
  return out;
}

/** Editor for provider-specific request fields (e.g. reasoning_effort, chat_template_kwargs). */
export function ExtraFields({ id, value, onChange }: { id: string; value: Record<string, unknown> | undefined; onChange: (v: Record<string, unknown> | undefined) => void }) {
  const [text, setText] = useState(value && Object.keys(value).length > 0 ? JSON.stringify(value, null, 2) : "");
  const [err, setErr] = useState<string | null>(null);
  return (
    <Row id={id} label="Extra request fields" hint={err ?? 'Sent as-is to the provider. One key: value per line (e.g. reasoning_effort: none), or a JSON object such as {"chat_template_kwargs": {"enable_thinking": false}}.'}>
      <Textarea id={id} rows={3} className="font-mono text-xs" value={text} aria-invalid={err !== null}
        onChange={(e) => {
          setText(e.target.value);
          if (!e.target.value.trim()) { setErr(null); onChange(undefined); return; }
          try {
            setErr(null);
            onChange(parseExtra(e.target.value));
          } catch (ex) {
            setErr(`Not valid: ${ex instanceof Error ? ex.message : String(ex)}`);
          }
        }} />
    </Row>
  );
}
