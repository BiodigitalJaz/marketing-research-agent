"use client";

import { useQuery } from "@tanstack/react-query";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api, unwrap } from "@/lib/api/client";

import { Section } from "./common";

export function RunsPanel() {
  const { data, refetch, isFetching } = useQuery({ queryKey: ["settings", "runs"], queryFn: async () => unwrap(await api.GET("/settings/runs")).items });
  return (
    <Section title="Recent model calls" description="Every call is logged with provider, tokens, cost, and latency. Prompt text is never stored here."
      actions={<Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching}>Refresh</Button>}>
      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full min-w-[640px] text-left text-sm">
          <thead className="bg-muted/50 text-xs text-muted-foreground">
            <tr><th className="p-2">When</th><th className="p-2">Task</th><th className="p-2">Provider / model</th><th className="p-2 text-right">Tokens in/out</th><th className="p-2 text-right">Cost</th><th className="p-2 text-right">Latency</th><th className="p-2">Status</th></tr>
          </thead>
          <tbody>
            {data?.length === 0 && <tr><td colSpan={7} className="p-3 text-muted-foreground">No calls yet.</td></tr>}
            {data?.map((r) => (
              <tr key={r.id} className="border-t">
                <td className="p-2 whitespace-nowrap">{new Date(r.created_at).toLocaleString()}</td>
                <td className="p-2">{r.task}</td>
                <td className="p-2 break-all">{r.provider_id} / {r.model} {r.fallback && <Badge variant="outline">fallback</Badge>}</td>
                <td className="p-2 text-right tabular-nums">{r.input_tokens ?? "–"} / {r.output_tokens ?? "–"}</td>
                <td className="p-2 text-right tabular-nums">{r.cost_usd != null ? `$${r.cost_usd.toFixed(4)}` : "–"}</td>
                <td className="p-2 text-right tabular-nums">{r.latency_ms != null ? `${r.latency_ms} ms` : "–"}</td>
                <td className="p-2">{r.error ? <span className="text-destructive" title={r.error}>error</span> : "ok"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Section>
  );
}
