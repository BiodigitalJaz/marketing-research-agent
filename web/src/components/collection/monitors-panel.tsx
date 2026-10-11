"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { useCrudMutation } from "@/components/brand/use-crud";
import { Check, Row, Section, Status } from "@/components/settings/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { api, unwrap, type Schemas } from "@/lib/api/client";
import { useSession } from "@/lib/session";

import { SOURCES_KEY, SourceSelect } from "./sources-panel";
import { MonitorProgress } from "./monitor-progress";

type Monitor = Schemas["Monitor"];
type Lane = Schemas["Lane"];
const KEY = ["monitors"] as const;
const LANES: { v: Lane; label: string }[] = [
  { v: "inbound", label: "Inbound" },
  { v: "media", label: "Media & speaking" },
  { v: "thought_leadership", label: "Thought leadership" },
];
const SCHEDULES = [
  { v: "*/15 * * * *", label: "Every 15 minutes" },
  { v: "*/30 * * * *", label: "Every 30 minutes" },
  { v: "@hourly", label: "Hourly" },
  { v: "0 */6 * * *", label: "Every 6 hours" },
  { v: "@daily", label: "Daily" },
];
type Form = { id?: string; name: string; source_ids: string[]; query: string; lanes: Lane[]; schedule_cron: string; enabled: boolean };

export function MonitorsPanel() {
  const { canEdit } = useSession();
  const { data } = useQuery({ queryKey: KEY, queryFn: async () => unwrap(await api.GET("/monitors")).items, refetchInterval: 30_000 });
  const { data: sources } = useQuery({ queryKey: SOURCES_KEY, queryFn: async () => unwrap(await api.GET("/sources")).items });
  const [editing, setEditing] = useState<Form | null>(null);

  const save = useCrudMutation(KEY, async (f: Form) => {
    const body = { name: f.name, source_ids: f.source_ids, query: f.query, lanes: f.lanes, schedule_cron: f.schedule_cron, enabled: f.enabled };
    return f.id ? unwrap(await api.PUT("/monitors/{id}", { params: { path: { id: f.id } }, body })) : unwrap(await api.POST("/monitors", { body }));
  }, "Monitor saved");
  const remove = useCrudMutation(KEY, async (id: string) => unwrap(await api.DELETE("/monitors/{id}", { params: { path: { id } } })), "Monitor deleted");
  const run = useCrudMutation(KEY, async (id: string) => unwrap(await api.POST("/monitors/{id}/run", { params: { path: { id } } })), "Collection started");

  const name = (id: string) => sources?.find((s) => s.id === id)?.name ?? "deleted source";
  const sched = (v: string) => SCHEDULES.find((s) => s.v === v)?.label ?? v;

  return (
    <Section title="Monitors" description="A monitor runs a set of sources on a schedule and keeps items matching its query (or your tracked keywords)."
      actions={canEdit && <Button onClick={() => setEditing({ name: "", source_ids: [], query: "", lanes: LANES.map((l) => l.v), schedule_cron: "*/30 * * * *", enabled: true })}>Add monitor</Button>}>
      <ul className="flex flex-col divide-y rounded-lg border">
        {data?.length === 0 && <li className="p-3 text-sm text-muted-foreground">No monitors yet.</li>}
        {data?.map((m: Monitor) => (
          <li key={m.id} className="flex flex-col gap-2 p-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0 text-sm">
              <p className="flex flex-wrap items-center gap-2 font-medium">
                {m.name}
                {m.enabled ? <Badge variant="secondary">{sched(m.schedule_cron)}</Badge> : <Badge variant="outline">Paused</Badge>}
                {m.last_run_at && <Status ok={!m.last_error}>{m.last_error ? "errors" : "ok"}</Status>}
              </p>
              <p className="text-xs text-muted-foreground">Query: {m.query || "tracked keywords"} · {m.source_ids.map(name).join(", ")}</p>
              <p className="text-xs text-muted-foreground">
                {m.last_run_at ? `Last run ${new Date(m.last_run_at).toLocaleString()}: ${m.last_new_items} new` : "Not run yet"}
                {m.enabled && m.next_run_at && ` · next ${new Date(m.next_run_at).toLocaleTimeString()}`}
              </p>
              {m.last_error && <p className="text-xs break-words text-destructive">{m.last_error}</p>}
              <div className="pt-1"><MonitorProgress id={m.id} /></div>
            </div>
            {canEdit && (
              <div className="flex shrink-0 gap-2">
                <Button variant="outline" size="sm" onClick={() => run.mutate(m.id)}>Run now</Button>
                <Button variant="outline" size="sm" onClick={() => setEditing({ id: m.id, name: m.name, source_ids: m.source_ids, query: m.query, lanes: m.lanes, schedule_cron: m.schedule_cron, enabled: m.enabled })}>Edit</Button>
                <Button variant="ghost" size="sm" onClick={() => confirm(`Delete ${m.name}?`) && remove.mutate(m.id)}>Delete</Button>
              </div>
            )}
          </li>
        ))}
      </ul>
      <Dialog open={editing !== null} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
          <DialogHeader><DialogTitle>{editing?.id ? `Edit ${editing.name}` : "Add monitor"}</DialogTitle></DialogHeader>
          {editing && (
            <form className="flex flex-col gap-4" onSubmit={(e) => { e.preventDefault(); save.mutate(editing, { onSuccess: () => setEditing(null) }); }}>
              <Row id="m-name" label="Name"><Input id="m-name" required value={editing.name} onChange={(e) => setEditing({ ...editing, name: e.target.value })} /></Row>
              <Row id="m-query" label="Query" hint={'Words must all appear; "quoted phrases"; OR for alternatives; -word to exclude. Empty uses your tracked keywords. Negative keywords always apply.'}>
                <Input id="m-query" placeholder='"project management" agency OR "client portal" -jobs' value={editing.query} onChange={(e) => setEditing({ ...editing, query: e.target.value })} />
              </Row>
              <Row id="m-src" label="Sources"><SourceSelect value={editing.source_ids} onChange={(ids) => setEditing({ ...editing, source_ids: ids })} sources={sources ?? []} /></Row>
              <fieldset className="flex flex-col gap-1.5">
                <legend className="text-sm font-medium">Lanes</legend>
                {LANES.map((l) => (
                  <Check key={l.v} id={`ml-${l.v}`} label={l.label} checked={editing.lanes.includes(l.v)}
                    onChange={(v) => setEditing({ ...editing, lanes: v ? [...editing.lanes, l.v] : editing.lanes.filter((x) => x !== l.v) })} />
                ))}
              </fieldset>
              <Row id="m-sched" label="Schedule">
                <NativeSelect id="m-sched" value={SCHEDULES.some((s) => s.v === editing.schedule_cron) ? editing.schedule_cron : "custom"}
                  onChange={(e) => setEditing({ ...editing, schedule_cron: e.target.value === "custom" ? "0 * * * *" : e.target.value })}>
                  {SCHEDULES.map((s) => <option key={s.v} value={s.v}>{s.label}</option>)}
                  <option value="custom">Custom cron…</option>
                </NativeSelect>
                {!SCHEDULES.some((s) => s.v === editing.schedule_cron) && (
                  <Input aria-label="Cron expression" value={editing.schedule_cron} onChange={(e) => setEditing({ ...editing, schedule_cron: e.target.value })} />
                )}
              </Row>
              <Check id="m-en" label="Enabled" checked={editing.enabled} onChange={(v) => setEditing({ ...editing, enabled: v })} />
              <DialogFooter><Button type="submit" disabled={save.isPending || editing.source_ids.length === 0}>{save.isPending ? "Saving…" : "Save"}</Button></DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </Section>
  );
}
