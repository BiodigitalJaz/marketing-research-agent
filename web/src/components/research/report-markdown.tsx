"use client";

import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

import type { Schemas } from "@/lib/api/client";

import { ChartView } from "./chart-view";

type Chart = Schemas["Chart"];
export type CitationRef = { marker: string; url: string; title?: string | null };

const CHART_LINE = /^\s*\{\{chart:([A-Za-z0-9_-]+)\}\}\s*$/;

/** Turns [n] citation markers into links to #cite-n (not Markdown link labels like [n](...)). */
function linkCitations(md: string): string {
  return md.replace(/\[(\d{1,3})\](?!\()/g, "[[$1]](#cite-$1)");
}

/** Renders report or answer Markdown: citations become hover cards, chart references become charts. */
export function ReportMarkdown({ markdown, charts = [], citations }: { markdown: string; charts?: Chart[]; citations: CitationRef[] }) {
  const byMarker = new Map(citations.map((c) => [c.marker, c]));
  const components: Components = {
    a: ({ href, children }) => {
      const m = href?.match(/^#cite-(\d+)$/);
      if (m) {
        const c = byMarker.get(`[${m[1]}]`);
        if (!c) return <span>{children}</span>;
        return (
          <span className="group relative inline-block align-baseline">
            <a href={c.url} target="_blank" rel="noreferrer noopener" className="rounded bg-muted px-1 text-xs font-medium no-underline hover:bg-primary hover:text-primary-foreground">
              {m[1]}
            </a>
            <span role="tooltip" className="invisible absolute bottom-full left-0 z-20 mb-1 w-64 rounded-md border bg-popover p-2 text-xs text-popover-foreground shadow-md group-focus-within:visible group-hover:visible">
              <span className="block font-medium">{c.title || c.url}</span>
              <span className="block truncate text-muted-foreground">{c.url}</span>
            </span>
          </span>
        );
      }
      return <a href={href} target="_blank" rel="noreferrer noopener" className="underline underline-offset-2">{children}</a>;
    },
  };
  // Split on chart lines so charts render between Markdown blocks.
  const blocks: ({ md: string } | { chart: string })[] = [];
  let buf: string[] = [];
  for (const line of markdown.split("\n")) {
    const m = line.match(CHART_LINE);
    if (m) {
      blocks.push({ md: buf.join("\n") }, { chart: m[1] });
      buf = [];
    } else buf.push(line);
  }
  blocks.push({ md: buf.join("\n") });
  return (
    <div className="prose-report text-sm leading-relaxed">
      {blocks.map((b, i) => {
        if ("chart" in b) {
          const c = charts.find((x) => x.id === b.chart);
          return c ? <ChartView key={i} chart={c} /> : null;
        }
        return b.md.trim() ? <ReactMarkdown key={i} remarkPlugins={[remarkGfm]} components={components}>{linkCitations(b.md)}</ReactMarkdown> : null;
      })}
    </div>
  );
}

export function SourcesList({ citations }: { citations: CitationRef[] }) {
  if (!citations.length) return null;
  return (
    <section className="flex flex-col gap-1">
      <h2 className="text-sm font-semibold">Sources</h2>
      <ol className="flex flex-col gap-1 text-xs">
        {citations.map((c) => (
          <li key={c.marker} id={`cite-${c.marker.slice(1, -1)}`} className="flex gap-2">
            <span className="w-7 shrink-0 text-muted-foreground tabular-nums">{c.marker}</span>
            <a href={c.url} target="_blank" rel="noreferrer noopener" className="break-all underline-offset-2 hover:underline">{c.title || c.url}</a>
          </li>
        ))}
      </ol>
    </section>
  );
}
