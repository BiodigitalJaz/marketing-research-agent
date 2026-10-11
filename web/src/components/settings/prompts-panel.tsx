"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { useCrudMutation } from "@/components/brand/use-crud";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { api, unwrap, type Schemas } from "@/lib/api/client";

import { PROMPT_TASKS, Row, Section } from "./common";

type Prompt = Schemas["Prompt"];

export function PromptsPanel() {
  const [task, setTask] = useState<string>("summarize");
  const key = ["settings", "prompts", task] as const;
  const { data } = useQuery({ queryKey: key, queryFn: async () => unwrap(await api.GET("/settings/prompts", { params: { query: { task } } })).items });
  const active = data?.find((p) => p.active);
  return (
    <Section title="Prompts" description="Edits are saved as new versions; activate any version to use it, or roll back to the shipped default.">
      <Row id="pr-task" label="Task" className="max-w-xs">
        <NativeSelect id="pr-task" value={task} onChange={(e) => setTask(e.target.value)}>
          {PROMPT_TASKS.map((t) => <option key={t} value={t}>{t === "draft_check" ? "draft_check (runs on the draft route)" : t}</option>)}
        </NativeSelect>
      </Row>
      <PromptEditor key={`${task}-${active?.id ?? "none"}`} task={task} base={active} queryKey={key} />
      <ul className="flex flex-col divide-y rounded-lg border">
        {data?.length === 0 && <li className="p-3 text-sm text-muted-foreground">No versions for this task yet. The feature using it ships in a later milestone.</li>}
        {data?.map((p) => <PromptRow key={p.id} prompt={p} queryKey={key} />)}
      </ul>
    </Section>
  );
}

function PromptEditor({ task, base, queryKey }: { task: string; base?: Prompt; queryKey: readonly unknown[] }) {
  const [body, setBody] = useState(base?.body ?? "");
  const [notes, setNotes] = useState("");
  const create = useCrudMutation(queryKey, async (activate: boolean) => unwrap(await api.POST("/settings/prompts", { body: { task, body, notes: notes || null, activate } })), "New version saved");
  return (
    <form className="flex flex-col gap-3" onSubmit={(e) => e.preventDefault()}>
      <Row id="pr-body" label={base ? `Editing from v${base.version} (${base.scope})` : "New prompt"}>
        <Textarea id="pr-body" rows={12} className="font-mono text-xs" value={body} onChange={(e) => setBody(e.target.value)} />
      </Row>
      <Row id="pr-notes" label="What changed">
        <Input id="pr-notes" value={notes} onChange={(e) => setNotes(e.target.value)} />
      </Row>
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" disabled={!body.trim() || create.isPending} onClick={() => create.mutate(false)}>Save as new version</Button>
        <Button type="button" disabled={!body.trim() || create.isPending} onClick={() => create.mutate(true)}>Save and activate</Button>
      </div>
    </form>
  );
}

function PromptRow({ prompt: p, queryKey }: { prompt: Prompt; queryKey: readonly unknown[] }) {
  const [open, setOpen] = useState(false);
  const activate = useCrudMutation(queryKey, async () => unwrap(await api.POST("/settings/prompts/{id}/activate", { params: { path: { id: p.id } } })), `v${p.version} activated`);
  return (
    <li className="flex flex-col gap-2 p-3 text-sm">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p>
          <span className="font-medium">v{p.version}</span>{" "}
          <span className="text-xs text-muted-foreground">{p.scope === "default" ? "shipped default" : "workspace"} · {new Date(p.created_at).toLocaleString()}</span>
          {p.active && <Badge className="ml-2">Active</Badge>}
          {p.notes && <span className="block text-xs text-muted-foreground">{p.notes}</span>}
        </p>
        <div className="flex gap-2">
          <Button variant="ghost" size="sm" onClick={() => setOpen(!open)}>{open ? "Hide" : "View"}</Button>
          {!p.active && <Button variant="outline" size="sm" onClick={() => activate.mutate()}>Activate</Button>}
        </div>
      </div>
      {open && <pre className="max-h-80 overflow-auto rounded-md bg-muted p-3 text-xs whitespace-pre-wrap">{p.body}</pre>}
    </li>
  );
}
