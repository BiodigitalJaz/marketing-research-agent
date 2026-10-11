"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { useCrudMutation } from "@/components/brand/use-crud";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { api, errorMessage, unwrap, type Schemas } from "@/lib/api/client";
import { useLocalOnly } from "@/lib/local-only";
import { cn } from "@/lib/utils";

import { Check, Row, Section } from "./common";
import { LocalOnlyNotice } from "./local-only-notice";

type Provider = Schemas["Provider"];
type ProviderInput = Schemas["ProviderInput"];
const KEY = ["settings", "providers"] as const;

type KeyMode = "keep" | "paste" | "env" | "none";
type Form = ProviderInput & { keyMode: KeyMode };

const NEW: Form = { id: "", name: "", kind: "openai_compat", base_url: "", is_local: false, enabled: true, headers: {}, options: {}, keyMode: "paste" };

function keyLabel(p: Provider) {
  const k = p.api_key;
  if (k.source === "secret") return { text: `Key ••••${k.last4 ?? ""} (encrypted)`, ok: true };
  if (k.source === "env") return { text: `env ${k.env} ${k.env_set ? "(set)" : "(not set)"}`, ok: k.env_set };
  return { text: p.is_local ? "No key (local)" : "No key", ok: p.is_local };
}

export function ProvidersPanel() {
  const { data, isPending } = useQuery({ queryKey: KEY, queryFn: async () => unwrap(await api.GET("/settings/providers")) });
  const [editing, setEditing] = useState<{ creating: boolean; form: Form } | null>(null);
  const [tests, setTests] = useState<Record<string, string>>({});
  const { blocked } = useLocalOnly();

  const save = useCrudMutation(
    KEY,
    async ({ creating, form }: { creating: boolean; form: Form }) => {
      const { keyMode, ...rest } = form;
      const body: ProviderInput = { ...rest };
      if (keyMode !== "paste") delete body.api_key;
      if (keyMode !== "env") body.api_key_env = "";
      if (keyMode === "none") body.clear_api_key = true;
      if (keyMode === "env") body.clear_api_key = true;
      return creating
        ? unwrap(await api.POST("/settings/providers", { body }))
        : unwrap(await api.PUT("/settings/providers/{id}", { params: { path: { id: form.id ?? "" } }, body }));
    },
    "Provider saved",
  );
  const remove = useCrudMutation(KEY, async (id: string) => unwrap(await api.DELETE("/settings/providers/{id}", { params: { path: { id } } })), "Provider deleted");

  async function test(id: string) {
    setTests((t) => ({ ...t, [id]: "Testing…" }));
    try {
      const r = unwrap(await api.POST("/settings/providers/{id}/test", { params: { path: { id } } }));
      setTests((t) => ({ ...t, [id]: r.ok ? `OK · ${r.models} models · ${r.latency_ms} ms` : `Failed: ${r.error ?? "unknown error"}` }));
    } catch (e) {
      setTests((t) => ({ ...t, [id]: `Failed: ${errorMessage(e)}` }));
    }
  }

  function edit(p: Provider) {
    setEditing({
      creating: false,
      form: { id: p.id, name: p.name, kind: p.kind, base_url: p.base_url, is_local: p.is_local, enabled: p.enabled, headers: p.headers, options: p.options,
        api_key_env: p.api_key.env ?? "", keyMode: p.api_key.source === "secret" ? "keep" : p.api_key.source === "env" ? "env" : "none" },
    });
  }

  return (
    <div className="flex flex-col gap-8">
      <Section
        title="Providers"
        description="Hosted and local endpoints. Hosted and local providers have the same capabilities; choose whichever model fits each task."
        actions={<Button onClick={() => setEditing({ creating: true, form: NEW })}>Add provider</Button>}
      >
        <LocalOnlyNotice />
        {data && !data.encryption_enabled && (
          <Alert>
            <AlertDescription>
              RESEARCH_AGENT_SECRETS_KEY is not set on the server, so keys can&apos;t be entered here. Reference an environment variable instead, or set the key and restart.
            </AlertDescription>
          </Alert>
        )}
        {isPending ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : (
          <ul className="flex flex-col divide-y rounded-lg border">
            {data?.items.map((p) => {
              const k = keyLabel(p);
              return (
                <li key={p.id} className={cn("flex flex-col gap-2 p-4 sm:flex-row sm:items-start sm:justify-between", blocked(p.id) && "opacity-60")}>
                  <div className="min-w-0">
                    <p className="flex flex-wrap items-center gap-2 font-medium">
                      {p.name || p.id}
                      <span className="text-xs font-normal text-muted-foreground">{p.id}</span>
                      <Badge variant="outline">{p.kind === "anthropic" ? "Anthropic" : "OpenAI-compatible"}</Badge>
                      {p.is_local && <Badge variant="secondary">Local</Badge>}
                      {blocked(p.id) && <Badge variant="outline">Off in local-only mode</Badge>}
                      {!p.enabled && <Badge variant="destructive">Disabled</Badge>}
                    </p>
                    {p.base_url && <p className="truncate text-xs text-muted-foreground">{p.base_url}</p>}
                    <p className={k.ok ? "text-xs text-muted-foreground" : "text-xs text-destructive"}>{k.text}</p>
                    {tests[p.id] && <p className="text-xs">{tests[p.id]}</p>}
                  </div>
                  <div className="flex shrink-0 flex-wrap gap-2">
                    <Button variant="outline" size="sm" onClick={() => test(p.id)}>Test</Button>
                    <Button variant="outline" size="sm" onClick={() => edit(p)}>Edit</Button>
                    <Button variant="ghost" size="sm" onClick={() => confirm(`Delete provider ${p.id}?`) && remove.mutate(p.id)}>Delete</Button>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </Section>

      <SecretsSection />

      <Dialog open={editing !== null} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>{editing?.creating ? "Add provider" : `Edit ${editing?.form.id}`}</DialogTitle>
          </DialogHeader>
          {editing && (
            <ProviderForm
              form={editing.form}
              creating={editing.creating}
              encryption={data?.encryption_enabled ?? false}
              onChange={(form) => setEditing({ ...editing, form })}
              onSubmit={() => save.mutate(editing, { onSuccess: () => setEditing(null) })}
              pending={save.isPending}
            />
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function ProviderForm({ form: f, creating, encryption, onChange, onSubmit, pending }: { form: Form; creating: boolean; encryption: boolean; onChange: (f: Form) => void; onSubmit: () => void; pending: boolean }) {
  const set = (patch: Partial<Form>) => onChange({ ...f, ...patch });
  const headers = Object.entries(f.headers ?? {});
  return (
    <form className="flex flex-col gap-4" onSubmit={(e) => { e.preventDefault(); onSubmit(); }}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Row id="p-id" label="ID" hint="Lowercase; used in routes.">
          <Input id="p-id" required disabled={!creating} value={f.id ?? ""} onChange={(e) => set({ id: e.target.value })} />
        </Row>
        <Row id="p-name" label="Display name">
          <Input id="p-name" value={f.name ?? ""} onChange={(e) => set({ name: e.target.value })} />
        </Row>
        <Row id="p-kind" label="Kind">
          <NativeSelect id="p-kind" value={f.kind} onChange={(e) => set({ kind: e.target.value as Form["kind"], is_local: e.target.value === "anthropic" ? false : f.is_local })}>
            <option value="openai_compat">OpenAI-compatible (OpenAI, LM Studio, llama.cpp, vLLM, Ollama)</option>
            <option value="anthropic">Anthropic</option>
          </NativeSelect>
        </Row>
        <Row id="p-url" label="Base URL" hint={f.kind === "anthropic" ? "Optional (proxies)." : "e.g. http://localhost:11434/v1 (Ollama) or http://localhost:1234/v1 (LM Studio)"}>
          <Input id="p-url" type="url" required={f.kind === "openai_compat"} value={f.base_url ?? ""} onChange={(e) => set({ base_url: e.target.value })} />
        </Row>
      </div>
      <div className="flex flex-wrap gap-6">
        <Check id="p-local" label="Local provider" hint="Runs on your hardware; allowed in local-only mode, costs $0." checked={f.is_local ?? false} disabled={f.kind === "anthropic"} onChange={(v) => set({ is_local: v })} />
        <Check id="p-enabled" label="Enabled" checked={f.enabled ?? true} onChange={(v) => set({ enabled: v })} />
      </div>

      <fieldset className="flex flex-col gap-2 rounded-lg border p-3">
        <legend className="px-1 text-sm font-medium">API key</legend>
        <div className="flex flex-wrap gap-4 text-sm">
          {!creating && f.keyMode === "keep" && <span className="text-muted-foreground">Keeping the current encrypted key.</span>}
          {(["paste", "env", "none"] as const).map((m) => (
            <label key={m} className="flex items-center gap-1.5">
              <input type="radio" name="keymode" checked={f.keyMode === m} onChange={() => set({ keyMode: m })} disabled={m === "paste" && !encryption} />
              {m === "paste" ? (creating ? "Paste key" : "Replace key") : m === "env" ? "Environment variable" : "No key"}
            </label>
          ))}
        </div>
        {f.keyMode === "paste" && (
          <Input aria-label="API key" type="password" autoComplete="off" placeholder="Stored encrypted; never shown again" value={f.api_key ?? ""} onChange={(e) => set({ api_key: e.target.value })} />
        )}
        {f.keyMode === "env" && <Input aria-label="Env var name" placeholder="ANTHROPIC_API_KEY" value={f.api_key_env ?? ""} onChange={(e) => set({ api_key_env: e.target.value })} />}
      </fieldset>

      {f.kind === "openai_compat" && (
        <div className="grid gap-4 sm:grid-cols-2">
          <Row id="p-rs" label="Thinking request style" hint="How reasoning is requested from this server.">
            <NativeSelect id="p-rs" value={f.options?.reasoning_style ?? ""} onChange={(e) => set({ options: { ...f.options, reasoning_style: (e.target.value || undefined) as never } })}>
              <option value="">reasoning_effort (OpenAI, LM Studio, Ollama)</option>
              <option value="chat_template">chat_template_kwargs (vLLM, SGLang, llama.cpp)</option>
            </NativeSelect>
          </Row>
          <Row id="p-mt" label="Max tokens field">
            <NativeSelect id="p-mt" value={f.options?.max_tokens_param ?? ""} onChange={(e) => set({ options: { ...f.options, max_tokens_param: (e.target.value || undefined) as never } })}>
              <option value="">Automatic</option>
              <option value="max_tokens">max_tokens</option>
              <option value="max_completion_tokens">max_completion_tokens</option>
            </NativeSelect>
          </Row>
        </div>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <Row id="p-retries" label="Retries on 429/5xx" hint="0 = default (3); -1 = none.">
          <Input id="p-retries" type="number" min={-1} max={10} value={f.options?.max_retries ?? 0} onChange={(e) => set({ options: { ...f.options, max_retries: Number(e.target.value) } })} />
        </Row>
        <Row id="p-conc" label="Max simultaneous calls" hint="0 = unlimited. Use 1 for a single local GPU.">
          <Input id="p-conc" type="number" min={0} max={64} value={(f.options as { max_concurrency?: number })?.max_concurrency ?? 0}
            onChange={(e) => set({ options: { ...f.options, max_concurrency: Number(e.target.value) } as never })} />
        </Row>
      </div>
      <Row id="p-h" label="Extra headers" hint="Non-secret only. Credentials go in the API key field.">
        <div className="flex flex-col gap-2">
          {headers.map(([k, v], i) => (
            <div key={i} className="flex gap-2">
              <Input aria-label="Header name" value={k} onChange={(e) => { const h = { ...f.headers }; delete h[k]; h[e.target.value] = v; set({ headers: h }); }} />
              <Input aria-label="Header value" value={v} onChange={(e) => set({ headers: { ...f.headers, [k]: e.target.value } })} />
              <Button type="button" variant="ghost" size="sm" onClick={() => { const h = { ...f.headers }; delete h[k]; set({ headers: h }); }}>Remove</Button>
            </div>
          ))}
          <Button type="button" variant="outline" size="sm" className="self-start" onClick={() => set({ headers: { ...f.headers, [`X-Header-${headers.length + 1}`]: "" } })}>Add header</Button>
        </div>
      </Row>
      <DialogFooter>
        <Button type="submit" disabled={pending}>{pending ? "Saving…" : "Save"}</Button>
      </DialogFooter>
    </form>
  );
}

function SecretsSection() {
  const key = ["settings", "secrets"] as const;
  const { data } = useQuery({ queryKey: key, queryFn: async () => unwrap(await api.GET("/settings/secrets")) });
  const [replacing, setReplacing] = useState<{ id: string; name: string; value: string } | null>(null);
  const update = useCrudMutation(key, async (r: { id: string; value: string }) => unwrap(await api.PUT("/settings/secrets/{id}", { params: { path: { id: r.id } }, body: { value: r.value } })), "Secret replaced");
  const remove = useCrudMutation(key, async (id: string) => unwrap(await api.DELETE("/settings/secrets/{id}", { params: { path: { id } } })), "Secret deleted");

  return (
    <Section title="Stored keys" description="Encrypted at rest. Values are write-only: you can replace a key, never read it back.">
      {data?.items.length === 0 ? (
        <p className="text-sm text-muted-foreground">No stored keys.</p>
      ) : (
        <ul className="flex flex-col divide-y rounded-lg border">
          {data?.items.map((s) => (
            <li key={s.id} className="flex flex-col gap-2 p-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="min-w-0 text-sm">
                <p className="font-medium break-words">{s.name}</p>
                <p className="text-xs text-muted-foreground">
                  ••••{s.last4} · {s.scope} · updated {new Date(s.updated_at).toLocaleString()}
                  {s.used_by_providers.length > 0 && ` · used by ${s.used_by_providers.join(", ")}`}
                </p>
              </div>
              <div className="flex gap-2">
                <Button variant="outline" size="sm" onClick={() => setReplacing({ id: s.id, name: s.name, value: "" })}>Replace</Button>
                <Button variant="ghost" size="sm" onClick={() => confirm(`Delete ${s.name}? Providers using it fall back to their env var, if any.`) && remove.mutate(s.id)}>Delete</Button>
              </div>
            </li>
          ))}
        </ul>
      )}
      <Dialog open={replacing !== null} onOpenChange={(o) => !o && setReplacing(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Replace {replacing?.name}</DialogTitle>
          </DialogHeader>
          {replacing && (
            <form className="flex flex-col gap-4" onSubmit={(e) => { e.preventDefault(); if (!replacing.value.trim()) { toast.error("Enter the new value"); return; } update.mutate({ id: replacing.id, value: replacing.value }, { onSuccess: () => setReplacing(null) }); }}>
              <Input aria-label="New value" type="password" autoComplete="off" value={replacing.value} onChange={(e) => setReplacing({ ...replacing, value: e.target.value })} />
              <DialogFooter>
                <Button type="submit" disabled={update.isPending}>Replace</Button>
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </Section>
  );
}
