"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { api, sessionHeaders, unwrap, type Schemas } from "@/lib/api/client";
import { OFF_IN_LOCAL_ONLY, useLocalOnly } from "@/lib/local-only";

import { Check, EFFORTS, num, Row, Section, TASKS } from "./common";
import { LocalOnlyNotice } from "./local-only-notice";

type Item = { kind: "text" | "thinking" | "tool_call" | "tool_result" | "error" | "done"; text: string };

async function* readSSE(res: Response): AsyncGenerator<{ event: string; data: string }> {
  const reader = res.body!.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let i;
    while ((i = buf.indexOf("\n\n")) >= 0) {
      const chunk = buf.slice(0, i);
      buf = buf.slice(i + 2);
      let event = "message";
      let data = "";
      for (const line of chunk.split("\n")) {
        if (line.startsWith("event: ")) event = line.slice(7);
        else if (line.startsWith("data: ")) data += line.slice(6);
      }
      yield { event, data };
    }
  }
}

function toBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result).split(",")[1] ?? "");
    r.onerror = () => reject(r.error);
    r.readAsDataURL(file);
  });
}

export function PlaygroundPanel() {
  const qc = useQueryClient();
  const { data: models } = useQuery({ queryKey: ["settings", "models"], queryFn: async () => unwrap(await api.GET("/settings/models")).items });
  const { data: mcp } = useQuery({ queryKey: ["settings", "mcp"], queryFn: async () => unwrap(await api.GET("/settings/mcp-servers")).items });
  const [mode, setMode] = useState<"model" | "task">("model");
  const { blocked } = useLocalOnly();
  const [target, setTarget] = useState("");
  const [task, setTask] = useState<string>("draft");
  const [system, setSystem] = useState("");
  const [message, setMessage] = useState("");
  const [images, setImages] = useState<File[]>([]);
  const [params, setParams] = useState<Schemas["Params"]>({});
  const [schema, setSchema] = useState("");
  const [servers, setServers] = useState<string[]>([]);
  const [items, setItems] = useState<Item[]>([]);
  const [running, setRunning] = useState(false);

  const selected = models?.find((m) => `${m.provider_id}::${m.model}` === target);
  const caps = selected?.capabilities;

  function push(kind: Item["kind"], text: string) {
    setItems((prev) => {
      const last = prev[prev.length - 1];
      if (last && last.kind === kind && (kind === "text" || kind === "thinking")) {
        return [...prev.slice(0, -1), { kind, text: last.text + text }];
      }
      return [...prev, { kind, text }];
    });
  }

  async function run() {
    setItems([]);
    setRunning(true);
    try {
      let jsonSchema: unknown = null;
      if (schema.trim()) jsonSchema = JSON.parse(schema);
      const body: Schemas["PlaygroundInput"] = {
        system: system || undefined,
        messages: [{ role: "user", text: message, images: await Promise.all(images.map(async (f) => ({ media_type: f.type, data: await toBase64(f) }))) }],
        params, mcp_server_ids: servers, json_schema: jsonSchema as never,
        ...(mode === "task" ? { task } : { provider_id: selected?.provider_id, model: selected?.model }),
      };
      const res = await fetch("/api/v1/settings/playground", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", ...sessionHeaders() }, body: JSON.stringify(body) });
      if (!res.ok || !res.body) {
        const err = await res.json().catch(() => null);
        push("error", err?.error?.message ?? res.statusText);
        return;
      }
      for await (const ev of readSSE(res)) {
        const d = JSON.parse(ev.data);
        if (ev.event === "text") push("text", d.text);
        else if (ev.event === "thinking") push("thinking", d.text);
        else if (ev.event === "tool_call") push("tool_call", `${d.name}(${JSON.stringify(d.args)})`);
        else if (ev.event === "tool_result") push("tool_result", d.content);
        else if (ev.event === "error") push("error", d.message);
        else if (ev.event === "done") push("done", `${d.model} · ${d.usage.input_tokens} in / ${d.usage.output_tokens} out · ${d.steps} step(s)${d.schema_valid ? " · schema valid" : d.schema_error ? ` · ${d.schema_error}` : ""}`);
      }
    } catch (e) {
      push("error", e instanceof Error ? e.message : String(e));
    } finally {
      setRunning(false);
      void qc.invalidateQueries({ queryKey: ["settings", "runs"] });
    }
  }

  return (
    <Section title="Playground" description="Try a model, parameters, prompt, and tools before changing a route. Calls are logged as task 'playground'.">
      <LocalOnlyNotice />
      <div className="grid gap-6 lg:grid-cols-2">
        <form className="flex flex-col gap-3" onSubmit={(e) => { e.preventDefault(); void run(); }}>
          <div className="flex gap-4 text-sm">
            <label className="flex items-center gap-1.5"><input type="radio" checked={mode === "model"} onChange={() => setMode("model")} />Specific model</label>
            <label className="flex items-center gap-1.5"><input type="radio" checked={mode === "task"} onChange={() => setMode("task")} />A task&apos;s route</label>
          </div>
          {mode === "model" ? (
            <Row id="pg-model" label="Model">
              <NativeSelect id="pg-model" value={target} onChange={(e) => setTarget(e.target.value)} required>
                <option value="" disabled>Choose</option>
                {models?.filter((m) => m.enabled).map((m) => <option key={`${m.provider_id}::${m.model}`} value={`${m.provider_id}::${m.model}`} disabled={blocked(m.provider_id)}>{m.provider_id} / {m.display_name || m.model}{blocked(m.provider_id) ? OFF_IN_LOCAL_ONLY : ""}</option>)}
              </NativeSelect>
            </Row>
          ) : (
            <Row id="pg-task" label="Task">
              <NativeSelect id="pg-task" value={task} onChange={(e) => setTask(e.target.value)}>{TASKS.map((t) => <option key={t} value={t}>{t}</option>)}</NativeSelect>
            </Row>
          )}
          <Row id="pg-system" label="System prompt"><Textarea id="pg-system" rows={4} value={system} onChange={(e) => setSystem(e.target.value)} /></Row>
          <Row id="pg-msg" label="Message"><Textarea id="pg-msg" rows={5} required value={message} onChange={(e) => setMessage(e.target.value)} /></Row>
          {(mode === "task" || caps?.vision) && (
            <Row id="pg-img" label="Images" hint="PNG, JPEG, GIF, or WebP.">
              <Input id="pg-img" type="file" accept="image/png,image/jpeg,image/gif,image/webp" multiple onChange={(e) => setImages(Array.from(e.target.files ?? []))} />
            </Row>
          )}
          <div className="grid grid-cols-2 gap-3">
            <Row id="pg-max" label="Max tokens"><Input id="pg-max" type="number" min={0} value={params.max_tokens ?? ""} onChange={(e) => setParams({ ...params, max_tokens: num(e.target.value) })} /></Row>
            {mode === "model" && caps?.sampling && (
              <Row id="pg-temp" label="Temperature"><Input id="pg-temp" type="number" step="0.05" min={0} max={2} value={params.temperature ?? ""} onChange={(e) => setParams({ ...params, temperature: num(e.target.value) })} /></Row>
            )}
            {mode === "model" && caps?.thinking === "effort" && (
              <Row id="pg-effort" label="Thinking effort">
                <NativeSelect id="pg-effort" value={params.thinking?.effort ?? ""} onChange={(e) => setParams({ ...params, thinking: e.target.value ? { effort: e.target.value as never, summarized: true } : undefined })}>
                  <option value="">Default</option>{EFFORTS.map((x) => <option key={x} value={x}>{x}</option>)}
                </NativeSelect>
              </Row>
            )}
            {mode === "model" && caps?.thinking === "budget" && (
              <Row id="pg-budget" label="Thinking budget"><Input id="pg-budget" type="number" min={1024} value={params.thinking?.budget_tokens ?? ""} onChange={(e) => { const b = num(e.target.value); setParams({ ...params, thinking: b ? { budget_tokens: b } : undefined }); }} /></Row>
            )}
          </div>
          {(mcp?.length ?? 0) > 0 && (
            <fieldset className="flex flex-col gap-1.5">
              <legend className="text-sm font-medium">MCP servers</legend>
              {mcp?.map((s) => <Check key={s.id} id={`pg-mcp-${s.id}`} label={s.name} disabled={!s.enabled || (mode === "model" && !caps?.tools)} checked={servers.includes(s.id)} onChange={(v) => setServers(v ? [...servers, s.id] : servers.filter((x) => x !== s.id))} />)}
            </fieldset>
          )}
          <Row id="pg-schema" label="JSON schema (optional)" hint="Requests structured output and validates the result.">
            <Textarea id="pg-schema" rows={3} className="font-mono text-xs" value={schema} onChange={(e) => setSchema(e.target.value)} />
          </Row>
          <Button type="submit" className="self-start" disabled={running}>{running ? "Running…" : "Run"}</Button>
        </form>
        <div className="flex min-h-40 flex-col gap-2 rounded-lg border p-3 text-sm">
          {items.length === 0 && <p className="text-muted-foreground">Output appears here.</p>}
          {items.map((it, i) => (
            <div key={i} className="flex flex-col gap-1">
              {it.kind !== "text" && <Badge variant={it.kind === "error" ? "destructive" : "outline"} className="self-start">{it.kind.replace("_", " ")}</Badge>}
              <pre className={`whitespace-pre-wrap break-words font-sans ${it.kind === "thinking" || it.kind === "tool_result" ? "text-xs text-muted-foreground" : ""}`}>{it.text}</pre>
            </div>
          ))}
        </div>
      </div>
    </Section>
  );
}
