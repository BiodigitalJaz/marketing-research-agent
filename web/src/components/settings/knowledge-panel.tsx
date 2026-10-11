"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, unwrap, type Schemas } from "@/lib/api/client";

import { Check, Row, Section, Status } from "./common";

type Source = Schemas["KnowledgeSource"];
const KEY = ["settings", "knowledge"] as const;

type Form = { id?: string; name: string; repo: string; branch: string; path_prefix: string; exclude: string; token: string; token_env: string; clear_token: boolean; enabled: boolean };

const blank = (exclude: string[]): Form => ({ name: "", repo: "", branch: "main", path_prefix: "docs/", exclude: exclude.join("\n"), token: "", token_env: "", clear_token: false, enabled: true });

/** Internal knowledge: docs synced read-only from a repository's main branch, and the memory bank generated from them. Research only. */
export function KnowledgePanel() {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: KEY, queryFn: async () => unwrap(await api.GET("/settings/knowledge")), refetchInterval: (q) => (q.state.data?.sources.some((s) => s.status === "syncing" || s.status === "building") ? 5000 : 30_000) });
  const [form, setForm] = useState<Form | null>(null);
  const refresh = () => void qc.invalidateQueries({ queryKey: KEY });

  const save = useMutation({
    mutationFn: async (f: Form) => {
      const body = {
        name: f.name, repo: f.repo, branch: f.branch, path_prefix: f.path_prefix, enabled: f.enabled,
        exclude: f.exclude.split("\n").map((x) => x.trim()).filter(Boolean),
        token: f.token || undefined, token_env: f.token_env || undefined, clear_token: f.clear_token || undefined,
      };
      return f.id ? unwrap(await api.PUT("/settings/knowledge/{id}", { params: { path: { id: f.id } }, body })) : unwrap(await api.POST("/settings/knowledge", { body }));
    },
    onSuccess: () => { toast.success(busy ? "Saved. Sync again when the current work finishes." : "Saved; syncing now"); setForm(null); refresh(); },
    onError: (e) => toast.error(errorMessage(e)),
  });
  const sync = useMutation({
    mutationFn: async ({ id, force }: { id: string; force: boolean }) => unwrap(await api.POST("/settings/knowledge/{id}/sync", { params: { path: { id }, query: { force } } })),
    onSuccess: () => { toast.success("Sync queued"); refresh(); }, onError: (e) => toast.error(errorMessage(e)),
  });
  const rebuild = useMutation({
    mutationFn: async () => unwrap(await api.POST("/settings/knowledge/rebuild")),
    onSuccess: () => { toast.success("Memory bank update queued"); refresh(); }, onError: (e) => toast.error(errorMessage(e)),
  });
  const remove = useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/settings/knowledge/{id}", { params: { path: { id } } })),
    onSuccess: () => { toast.success("Removed"); refresh(); }, onError: (e) => toast.error(errorMessage(e)),
  });

  const counts = data?.counts;
  // One sync or build at a time per workspace; the server enforces it too.
  const busy = data?.sources.some((s) => s.status === "syncing" || s.status === "building") ?? false;
  return (
    <div className="flex flex-col gap-6">
      <Section title="Memory bank" description="The project's own docs, synced read-only from a repository's main branch when you choose Sync now, plus six core summary files generated from them (after Cline's memory bank). Research uses them for questions about the project itself; drafting never sees them."
        actions={<Link href="/memory" className="text-sm underline underline-offset-2">Read the memory bank</Link>}>
        {counts && (
          <p className="text-sm text-muted-foreground">
            {counts.docs} docs · {counts.chunks} sections · {counts.embedded} embedded · {counts.digested} summarized · memory bank files: {data.memory_bank.length} of {data.core_files.length}
          </p>
        )}
        <ul className="flex flex-col gap-3">
          {data?.sources.map((s) => <SourceRow key={s.id} source={s} busy={busy || sync.isPending} onEdit={() => setForm(toForm(s))} onSync={() => sync.mutate({ id: s.id, force: false })} onRemove={() => { if (confirm(`Remove ${s.name} and its synced docs?`)) remove.mutate(s.id); }} />)}
        </ul>
        <div className="flex flex-wrap gap-2">
          {!form && <Button size="sm" variant="outline" onClick={() => setForm(blank(data?.default_exclude ?? []))}>Add a repository</Button>}
          {data && data.sources.length > 0 && <Button size="sm" variant="ghost" disabled={busy || rebuild.isPending} title={busy ? "A sync or build is in progress" : undefined} onClick={() => rebuild.mutate()}>Update memory bank now</Button>}
        </div>
        {form && (
          <form className="flex flex-col gap-3 rounded-lg border p-4" onSubmit={(e) => { e.preventDefault(); save.mutate(form); }}>
            <div className="grid gap-3 sm:grid-cols-2">
              <Row id="k-repo" label="Repository" hint="owner/name on GitHub"><Input id="k-repo" required placeholder="your-org/your-repo" value={form.repo} onChange={(e) => setForm({ ...form, repo: e.target.value })} /></Row>
              <Row id="k-name" label="Name"><Input id="k-name" placeholder="Product docs" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} /></Row>
              <Row id="k-branch" label="Branch" hint="Only this branch is read."><Input id="k-branch" value={form.branch} onChange={(e) => setForm({ ...form, branch: e.target.value })} /></Row>
              <Row id="k-path" label="Docs directory"><Input id="k-path" value={form.path_prefix} onChange={(e) => setForm({ ...form, path_prefix: e.target.value })} /></Row>
            </div>
            <Row id="k-ex" label="Exclude" hint="One glob per line, relative to the docs directory. dir/** excludes a directory. Only Markdown files are read.">
              <Textarea id="k-ex" rows={3} className="font-mono text-xs" value={form.exclude} onChange={(e) => setForm({ ...form, exclude: e.target.value })} />
            </Row>
            <div className="grid gap-3 sm:grid-cols-2">
              <Row id="k-tok" label="Read-only token" hint="A fine-grained GitHub token with read access to Contents on this repository only. Stored encrypted; never shown again.">
                <Input id="k-tok" type="password" autoComplete="off" placeholder={form.id ? "Leave blank to keep the current token" : "github_pat_…"} value={form.token} onChange={(e) => setForm({ ...form, token: e.target.value })} />
              </Row>
              <Row id="k-env" label="…or token env var" hint="Name of an environment variable on the worker.">
                <Input id="k-env" placeholder="DOCS_GITHUB_TOKEN" value={form.token_env} onChange={(e) => setForm({ ...form, token_env: e.target.value })} />
              </Row>
            </div>
            {form.id && <Check id="k-clear" label="Remove the stored token" checked={form.clear_token} onChange={(v) => setForm({ ...form, clear_token: v })} />}
            <div className="flex gap-2">
              <Button type="submit" size="sm" disabled={save.isPending}>{save.isPending ? "Saving…" : "Save and sync"}</Button>
              <Button type="button" size="sm" variant="ghost" onClick={() => setForm(null)}>Cancel</Button>
            </div>
          </form>
        )}
      </Section>
    </div>
  );
}

function toForm(s: Source): Form {
  return { id: s.id, name: s.name, repo: s.repo, branch: s.branch, path_prefix: s.path_prefix, exclude: s.exclude.join("\n"), token: "", token_env: s.token.env ?? "", clear_token: false, enabled: s.enabled };
}

function SourceRow({ source: s, busy: anyBusy, onEdit, onSync, onRemove }: { source: Source; busy: boolean; onEdit: () => void; onSync: () => void; onRemove: () => void }) {
  const [open, setOpen] = useState(false);
  const { data: docs } = useQuery({ queryKey: ["settings", "knowledge", s.id, "docs"], enabled: open, queryFn: async () => unwrap(await api.GET("/settings/knowledge/{id}/docs", { params: { path: { id: s.id } } })).items });
  const busy = s.status === "syncing" || s.status === "building";
  return (
    <li className="flex flex-col gap-2 rounded-lg border p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <p className="font-medium">{s.name}</p>
          <p className="text-xs text-muted-foreground">
            {s.repo} · {s.branch} · {s.path_prefix || "(root)"} · token: {s.token.source === "secret" ? `stored ••••${s.token.last4 ?? ""}` : s.token.source === "env" ? `${s.token.env} (${s.token.env_set ? "set" : "not set"})` : "none"}
          </p>
          <p className="text-xs text-muted-foreground">
            {s.last_commit ? `At ${s.last_commit.slice(0, 7)}` : "Not synced yet"}{s.last_synced_at && ` · checked ${new Date(s.last_synced_at).toLocaleString()}`}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {busy ? <Badge variant="outline">{s.status}…</Badge> : <Status ok={s.status !== "error"}>{s.status === "error" ? "Error" : "Up to date as of last sync"}</Status>}
        </div>
      </div>
      {s.last_error && <p className="text-xs text-destructive">{s.last_error}</p>}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="outline" disabled={anyBusy} title={anyBusy ? "A sync or build is in progress" : "Check the branch for doc changes"} onClick={onSync}>Sync now</Button>
        <Button size="sm" variant="ghost" onClick={onEdit}>Edit</Button>
        <Button size="sm" variant="ghost" onClick={() => setOpen(!open)}>{open ? "Hide docs" : "Show docs"}</Button>
        <Button size="sm" variant="ghost" onClick={onRemove}>Remove</Button>
      </div>
      {open && (
        <ul className="flex flex-col divide-y rounded-md border text-xs">
          {docs?.length === 0 && <li className="p-2 text-muted-foreground">No docs synced yet.</li>}
          {docs?.map((d) => (
            <li key={d.path} className="flex flex-wrap items-center justify-between gap-2 p-2">
              <a href={d.url} target="_blank" rel="noreferrer noopener" className="font-medium hover:underline">{d.path}</a>
              <span className="text-muted-foreground">{d.chunks} sections · {d.digested ? `informs ${d.informs.join(", ") || "—"}` : "not summarized yet"}</span>
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}
