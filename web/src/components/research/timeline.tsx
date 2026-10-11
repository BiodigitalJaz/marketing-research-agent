"use client";

import Link from "next/link";

import { Badge } from "@/components/ui/badge";
import type { Schemas } from "@/lib/api/client";
import { cn } from "@/lib/utils";

type Step = Schemas["ResearchStep"];
type P = Record<string, unknown>;

const TOOL_LABEL: Record<string, string> = {
  search_corpus: "Searched collected content",
  get_item: "Read an item",
  search_brand: "Checked brand profile",
  opportunity_stats: "Counted opportunities",
  make_chart: "Made a chart",
  web_search: "Searched the web",
  fetch_url: "Read a page",
  view_image: "Looked at an image",
};

function argSummary(args: unknown): string {
  if (!args || typeof args !== "object") return "";
  const a = args as P;
  const v = a.query ?? a.url ?? a.title ?? a.group_by ?? (a.source != null ? `[${a.source}]` : undefined);
  return v == null ? "" : String(v);
}

/** The live progress of a research job: plan, tool calls, notes, verification, report. */
export function Timeline({ steps, compact = false }: { steps: Step[]; compact?: boolean }) {
  const results = new Map<string, P>();
  for (const s of steps) if (s.kind === "tool_result") results.set(String((s.payload as P).id), s.payload as P);
  return (
    <ol className="flex flex-col gap-2 text-sm">
      {steps.map((s) => {
        const p = s.payload as P;
        switch (s.kind) {
          case "plan":
            return (
              <li key={s.seq} className="rounded-lg border p-3">
                <p className="font-medium">Plan: {String(p.title ?? "")}</p>
                <ol className="mt-1 list-decimal pl-5 text-muted-foreground">{(p.sub_questions as string[] | undefined)?.map((q, i) => <li key={i}>{q}</li>)}</ol>
              </li>
            );
          case "phase":
            return <li key={s.seq} className="pt-2 text-xs font-semibold tracking-wide text-muted-foreground uppercase">Researching {String(p.index)} of {String(p.of)}: <span className="normal-case">{String(p.sub_question)}</span></li>;
          case "tool_call": {
            const r = results.get(String(p.id));
            const err = r?.is_error === true;
            return (
              <li key={s.seq} className="flex flex-wrap items-baseline gap-x-2 pl-3">
                <span className={cn("size-1.5 shrink-0 translate-y-[-1px] rounded-full", !r ? "animate-pulse bg-primary" : err ? "bg-destructive" : "bg-emerald-600")} aria-hidden />
                <span>{TOOL_LABEL[String(p.name)] ?? String(p.name)}</span>
                {argSummary(p.args) && <span className="text-muted-foreground">“{argSummary(p.args)}”</span>}
                {r && <span className={cn("text-xs", err ? "text-destructive" : "text-muted-foreground")}>{err ? String(r.summary ?? "failed") : String(r.summary ?? "")}</span>}
              </li>
            );
          }
          case "tool_result":
            return null;
          case "thought_summary":
            return compact ? null : <li key={s.seq} className="pl-3 text-xs text-muted-foreground italic">{String(p.text ?? "")}</li>;
          case "assistant":
            return compact ? null : <li key={s.seq} className="pl-3 text-xs text-muted-foreground">{String(p.text ?? "")}</li>;
          case "note":
            return (
              <li key={s.seq} className="rounded-lg border bg-muted/30 p-3">
                <details>
                  <summary className="cursor-pointer text-xs font-medium">Notes for “{String(p.sub_question)}”</summary>
                  <p className="mt-2 text-xs whitespace-pre-wrap">{String(p.text ?? "")}</p>
                </details>
              </li>
            );
          case "verify": {
            const claims = (p.claims as { claim: string; sources: number[]; single_source: boolean }[] | undefined) ?? [];
            const unsupported = (p.unsupported as { claim: string; sources: number[]; note?: string }[] | undefined) ?? [];
            return (
              <li key={s.seq} className="rounded-lg border p-3">
                <p className="font-medium">
                  Checked {claims.length + unsupported.length} key claims against their sources
                  {unsupported.length > 0 && <span className="font-normal text-muted-foreground"> · {unsupported.length} not supported</span>}
                </p>
                <ul className="mt-1 flex flex-col gap-1 text-xs">
                  {claims.map((c, i) => (
                    <li key={i}>{c.claim} <span className="text-muted-foreground">{c.sources.map((n) => `[${n}]`).join("")}</span>{c.single_source && <Badge variant="outline" className="ml-1">single source</Badge>}</li>
                  ))}
                  {unsupported.map((c, i) => (
                    <li key={`u${i}`}>
                      <Badge variant="outline" className="mr-1 border-amber-500/60">not supported</Badge>
                      {c.claim} <span className="text-muted-foreground">{c.sources.map((n) => `[${n}]`).join("")}{c.note ? ` — ${c.note}` : ""}</span>
                    </li>
                  ))}
                </ul>
              </li>
            );
          }
          case "limit":
            return <li key={s.seq} className="rounded-md border border-amber-500/40 bg-amber-50 px-3 py-1.5 text-xs text-amber-900 dark:bg-amber-950 dark:text-amber-100">Reached the {String(p.reason).replace("_", " ")}; answering from what was gathered.</li>;
          case "warning":
            return <li key={s.seq} className="rounded-md border border-amber-500/40 px-3 py-1.5 text-xs">{String(p.text ?? "")}</li>;
          case "report":
            return (
              <li key={s.seq} className="rounded-lg border border-emerald-600/40 p-3">
                Report ready: <Link href={`/reports/${String(p.report_id)}`} className="font-medium underline underline-offset-2">{String(p.title)}</Link>
                <span className="ml-2 text-xs text-muted-foreground">{String(p.citations)} citations · {String(p.charts)} charts</span>
              </li>
            );
          case "done":
            return p.status === "failed" ? <li key={s.seq} className="text-xs text-destructive">Failed: {String(p.error ?? "")}</li>
              : p.status === "cancelled" ? <li key={s.seq} className="text-xs text-muted-foreground">Cancelled.</li> : null;
          default:
            return null;
        }
      })}
    </ol>
  );
}
