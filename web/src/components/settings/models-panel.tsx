"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { useCrudMutation } from "@/components/brand/use-crud";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { api, errorMessage, unwrap, type Schemas } from "@/lib/api/client";
import { useLocalOnly } from "@/lib/local-only";
import { cn } from "@/lib/utils";

import { Check, EFFORTS, num, Row, Section } from "./common";
import { ExtraFields } from "./extra-fields";

type Model = Schemas["Model"];
type Caps = Schemas["Capabilities"];
const KEY = ["settings", "models"] as const;
const EMPTY_CAPS: Caps = { tools: true, vision: false, json_schema: true, sampling: true, thinking: "" };

export function blankModel(provider: string, id: string, caps?: Caps): Model {
  return { provider_id: provider, model: id, display_name: "", capabilities: caps ?? EMPTY_CAPS, default_params: {}, price: {}, enabled: true };
}

export function capsSummary(c: Caps) {
  return [c.tools && "tools", c.vision && "vision", c.json_schema && "JSON", c.thinking && `thinking (${c.thinking})`, c.sampling && "sampling"].filter(Boolean).join(" · ");
}

export function ModelsPanel() {
  const { data: models } = useQuery({ queryKey: KEY, queryFn: async () => unwrap(await api.GET("/settings/models")).items });
  const { data: providers } = useQuery({ queryKey: ["settings", "providers"], queryFn: async () => unwrap(await api.GET("/settings/providers")) });
  const [editing, setEditing] = useState<Model | null>(null);
  const { blocked } = useLocalOnly();
  const [discover, setDiscover] = useState<{ provider: string; items: Schemas["DiscoveredModel"][] } | null>(null);

  const save = useCrudMutation(KEY, async (m: Model) => unwrap(await api.POST("/settings/models", { body: m })), "Model saved");
  const remove = useCrudMutation(KEY, async (m: Model) => unwrap(await api.DELETE("/settings/models", { params: { query: { provider_id: m.provider_id, model: m.model } } })), "Model removed");

  async function runDiscover(provider: string) {
    try {
      const r = unwrap(await api.GET("/settings/providers/{id}/discover", { params: { path: { id: provider } } }));
      setDiscover({ provider, items: r.items });
    } catch (e) {
      toast.error(errorMessage(e));
    }
  }

  return (
    <Section
      title="Models"
      description="Declare what each model supports. Capabilities only validate configuration; hosted and local models run through the same code."
      actions={
        <div className="flex flex-wrap gap-2">
          {providers?.items.map((p) => (
            <Button key={p.id} variant="outline" size="sm" onClick={() => runDiscover(p.id)}>Discover on {p.id}</Button>
          ))}
          <Button size="sm" onClick={() => setEditing(blankModel(providers?.items[0]?.id ?? "", ""))}>Add model</Button>
        </div>
      }
    >
      <ul className="flex flex-col divide-y rounded-lg border">
        {models?.map((m) => (
          <li key={`${m.provider_id}/${m.model}`} className={cn("flex flex-col gap-2 p-3 sm:flex-row sm:items-center sm:justify-between", blocked(m.provider_id) && "opacity-60")}>
            <div className="min-w-0 text-sm">
              <p className="font-medium break-all">
                {m.display_name || m.model} <span className="text-xs font-normal text-muted-foreground">{m.provider_id}/{m.model}</span>
                {!m.enabled && <Badge variant="destructive" className="ml-2">Disabled</Badge>}
                {blocked(m.provider_id) && <Badge variant="outline" className="ml-2">Off in local-only mode</Badge>}
              </p>
              <p className="text-xs text-muted-foreground">
                {capsSummary(m.capabilities) || "no capabilities declared"}
                {(m.price.input_per_mtok ?? 0) > 0 && ` · $${m.price.input_per_mtok}/$${m.price.output_per_mtok} per MTok`}
              </p>
            </div>
            <div className="flex gap-2">
              <Button variant="outline" size="sm" onClick={() => setEditing(m)}>Edit</Button>
              <Button variant="ghost" size="sm" onClick={() => confirm(`Remove ${m.model}?`) && remove.mutate(m)}>Remove</Button>
            </div>
          </li>
        ))}
      </ul>

      <Dialog open={discover !== null} onOpenChange={(o) => !o && setDiscover(null)}>
        <DialogContent className="max-h-[80vh] overflow-y-auto sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Models on {discover?.provider}</DialogTitle>
          </DialogHeader>
          {discover?.items.length === 0 && <p className="text-sm text-muted-foreground">The endpoint returned no models. Is a model loaded?</p>}
          <ul className="flex flex-col divide-y">
            {discover?.items.map((d) => {
              const declared = models?.some((m) => m.provider_id === discover.provider && m.model === d.id);
              return (
                <li key={d.id} className="flex items-center justify-between gap-2 py-2 text-sm">
                  <span className="break-all">{d.display_name || d.id}</span>
                  <Button size="sm" variant={declared ? "ghost" : "outline"} disabled={declared}
                    onClick={() => { setEditing(blankModel(discover.provider, d.id, d.capabilities)); setDiscover(null); }}>
                    {declared ? "Declared" : "Declare"}
                  </Button>
                </li>
              );
            })}
          </ul>
        </DialogContent>
      </Dialog>

      <Dialog open={editing !== null} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>{editing?.model ? editing.model : "Add model"}</DialogTitle>
          </DialogHeader>
          {editing && (
            <ModelForm model={editing} providers={providers?.items.map((p) => p.id) ?? []} onChange={setEditing}
              onSubmit={() => save.mutate(editing, { onSuccess: () => setEditing(null) })} pending={save.isPending} />
          )}
        </DialogContent>
      </Dialog>
    </Section>
  );
}

function ModelForm({ model: m, providers, onChange, onSubmit, pending }: { model: Model; providers: string[]; onChange: (m: Model) => void; onSubmit: () => void; pending: boolean }) {
  const caps = m.capabilities;
  const setCaps = (patch: Partial<Caps>) => onChange({ ...m, capabilities: { ...caps, ...patch } });
  const p = m.default_params;
  const setP = (patch: Partial<Model["default_params"]>) => onChange({ ...m, default_params: { ...p, ...patch } });
  return (
    <form className="flex flex-col gap-4" onSubmit={(e) => { e.preventDefault(); onSubmit(); }}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Row id="m-provider" label="Provider">
          <NativeSelect id="m-provider" value={m.provider_id} onChange={(e) => onChange({ ...m, provider_id: e.target.value })}>
            {providers.map((id) => <option key={id} value={id}>{id}</option>)}
          </NativeSelect>
        </Row>
        <Row id="m-model" label="Model ID" hint="Exactly as the endpoint expects it.">
          <Input id="m-model" required value={m.model} onChange={(e) => onChange({ ...m, model: e.target.value })} />
        </Row>
        <Row id="m-name" label="Display name">
          <Input id="m-name" value={m.display_name} onChange={(e) => onChange({ ...m, display_name: e.target.value })} />
        </Row>
        <Check id="m-enabled" label="Enabled" checked={m.enabled} onChange={(v) => onChange({ ...m, enabled: v })} />
      </div>
      <fieldset className="grid gap-3 rounded-lg border p-3 sm:grid-cols-2">
        <legend className="px-1 text-sm font-medium">Capabilities</legend>
        <Check id="c-tools" label="Tool calls" checked={caps.tools} onChange={(v) => setCaps({ tools: v })} />
        <Check id="c-vision" label="Vision (image input)" checked={caps.vision} onChange={(v) => setCaps({ vision: v })} />
        <Check id="c-json" label="Structured output (JSON schema)" checked={caps.json_schema} onChange={(v) => setCaps({ json_schema: v })} />
        <Check id="c-sampling" label="Accepts temperature / top_p" checked={caps.sampling} onChange={(v) => setCaps({ sampling: v })} />
        <Row id="c-thinking" label="Thinking">
          <NativeSelect id="c-thinking" value={caps.thinking ?? ""} onChange={(e) => setCaps({ thinking: e.target.value as Caps["thinking"] })}>
            <option value="">None</option>
            <option value="effort">Effort levels (low … max)</option>
            <option value="budget">Token budget</option>
          </NativeSelect>
        </Row>
        <div className="grid grid-cols-2 gap-2">
          <Row id="c-ctx" label="Context window">
            <Input id="c-ctx" type="number" min={0} value={caps.max_context ?? ""} onChange={(e) => setCaps({ max_context: num(e.target.value) })} />
          </Row>
          <Row id="c-out" label="Max output">
            <Input id="c-out" type="number" min={0} value={caps.max_output ?? ""} onChange={(e) => setCaps({ max_output: num(e.target.value) })} />
          </Row>
        </div>
      </fieldset>
      <fieldset className="grid gap-3 rounded-lg border p-3 sm:grid-cols-2">
        <legend className="px-1 text-sm font-medium">Default parameters</legend>
        <Row id="d-max" label="Max tokens">
          <Input id="d-max" type="number" min={0} value={p.max_tokens ?? ""} onChange={(e) => setP({ max_tokens: num(e.target.value) })} />
        </Row>
        {caps.sampling && (
          <Row id="d-temp" label="Temperature">
            <Input id="d-temp" type="number" step="0.05" min={0} max={2} value={p.temperature ?? ""} onChange={(e) => setP({ temperature: num(e.target.value) })} />
          </Row>
        )}
        {caps.thinking === "effort" && (
          <Row id="d-effort" label="Thinking effort">
            <NativeSelect id="d-effort" value={p.thinking?.effort ?? ""} onChange={(e) => setP({ thinking: e.target.value ? { effort: e.target.value as never } : undefined })}>
              <option value="">Model default</option>
              {EFFORTS.map((x) => <option key={x} value={x}>{x}</option>)}
            </NativeSelect>
          </Row>
        )}
        {caps.thinking === "budget" && (
          <Row id="d-budget" label="Thinking budget (tokens)" hint="Minimum 1024; empty = off.">
            <Input id="d-budget" type="number" min={1024} value={p.thinking?.budget_tokens ?? ""} onChange={(e) => { const b = num(e.target.value); setP({ thinking: b ? { budget_tokens: b } : undefined }); }} />
          </Row>
        )}
        <div className="sm:col-span-2"><ExtraFields id="d-extra" value={p.extra} onChange={(v) => setP({ extra: v })} /></div>
      </fieldset>
      <div className="grid gap-4 sm:grid-cols-2">
        <Row id="pr-in" label="Input $ / MTok">
          <Input id="pr-in" type="number" step="0.01" min={0} value={m.price.input_per_mtok ?? ""} onChange={(e) => onChange({ ...m, price: { ...m.price, input_per_mtok: num(e.target.value) } })} />
        </Row>
        <Row id="pr-out" label="Output $ / MTok" hint="Local providers always cost $0.">
          <Input id="pr-out" type="number" step="0.01" min={0} value={m.price.output_per_mtok ?? ""} onChange={(e) => onChange({ ...m, price: { ...m.price, output_per_mtok: num(e.target.value) } })} />
        </Row>
      </div>
      <DialogFooter>
        <Button type="submit" disabled={pending}>{pending ? "Saving…" : "Save"}</Button>
      </DialogFooter>
    </form>
  );
}
