"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { Section } from "@/components/settings/common";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { useCrudMutation } from "@/components/brand/use-crud";
import { api, unwrap } from "@/lib/api/client";
import { useSession } from "@/lib/session";

import { SOURCES_KEY } from "./sources-panel";

export function ItemsPanel() {
  const [source, setSource] = useState("");
  const { canEdit } = useSession();
  const process = useCrudMutation(["opportunities"], async (rescore: boolean) => unwrap(await api.POST("/items/process", { params: { query: { rescore } } })), "Scoring queued");
  const { data: sources } = useQuery({ queryKey: SOURCES_KEY, queryFn: async () => unwrap(await api.GET("/sources")).items });
  const { data, refetch, isFetching } = useQuery({
    queryKey: ["items", source],
    queryFn: async () => unwrap(await api.GET("/items", { params: { query: { source_id: source || undefined, limit: 100 } } })),
  });
  return (
    <Section title="Collected items" description={`Raw content before scoring. ${data ? `${data.total} items stored.` : ""}`}
      actions={
        <div className="flex gap-2">
          <NativeSelect aria-label="Filter by source" className="w-56" value={source} onChange={(e) => setSource(e.target.value)}>
            <option value="">All sources</option>
            {sources?.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </NativeSelect>
          <Button variant="outline" size="sm" onClick={() => refetch()} disabled={isFetching}>Refresh</Button>
          {canEdit && <Button variant="outline" size="sm" onClick={() => process.mutate(false)} title="Score items that have not been processed yet">Score pending</Button>}
          {canEdit && <Button variant="outline" size="sm" onClick={() => process.mutate(true)} title="Re-score the last 14 days of items that produced no opportunity (e.g. after changing models or targeting)">Re-score recent</Button>}
        </div>
      }>
      <ul className="flex flex-col divide-y rounded-lg border">
        {data?.items.length === 0 && <li className="p-3 text-sm text-muted-foreground">Nothing collected yet. Enable a monitor or use Run now.</li>}
        {data?.items.map((it) => (
          <li key={it.id} className="flex flex-col gap-0.5 p-3 text-sm">
            <a href={it.url} target="_blank" rel="noreferrer noopener" className="font-medium break-words underline-offset-2 hover:underline">{it.title || it.url}</a>
            <p className="text-xs text-muted-foreground">
              {it.source_name}{it.community && it.community !== it.source_name && ` · ${it.community}`}
              {it.published_at && ` · published ${new Date(it.published_at).toLocaleString()}`} · collected {new Date(it.fetched_at).toLocaleString()}
            </p>
            {it.snippet && <p className="line-clamp-2 text-xs text-muted-foreground">{it.snippet}</p>}
          </li>
        ))}
      </ul>
    </Section>
  );
}
