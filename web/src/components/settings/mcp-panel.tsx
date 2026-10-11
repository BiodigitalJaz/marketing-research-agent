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

import { Check, Row, Section } from "./common";

type Server = Schemas["MCPServer"];
type Tool = Schemas["MCPTool"];
const KEY = ["settings", "mcp"] as const;

type HeaderRow = { name: string; mode: "keep" | "value" | "env"; value: string };
type Form = { id?: string; name: string; transport: "streamable_http" | "stdio"; url: string; command: string; args: string; headers: HeaderRow[]; allowed: string[]; enabled: boolean };

const NEW: Form = { name: "", transport: "streamable_http", url: "", command: "", args: "", headers: [], allowed: [], enabled: true };

export function MCPPanel() {
  const { data } = useQuery({ queryKey: KEY, queryFn: async () => unwrap(await api.GET("/settings/mcp-servers")).items });
  const [editing, setEditing] = useState<Form | null>(null);
  const [tools, setTools] = useState<Tool[] | null>(null);
  const remove = useCrudMutation(KEY, async (id: string) => unwrap(await api.DELETE("/settings/mcp-servers/{id}", { params: { path: { id } } })), "Server removed");

  const save = useCrudMutation(KEY, async (f: Form) => {
    const headers: Record<string, { env?: string; value?: string }> = {};
    for (const h of f.headers) {
      if (!h.name.trim()) continue;
      headers[h.name] = h.mode === "env" ? { env: h.value } : h.mode === "value" ? { value: h.value } : {};
    }
    const body = { name: f.name, transport: f.transport, url: f.url, command: f.command, args: f.args.split(" ").filter(Boolean), headers, allowed_tools: f.allowed, enabled: f.enabled };
    return f.id
      ? unwrap(await api.PUT("/settings/mcp-servers/{id}", { params: { path: { id: f.id } }, body }))
      : unwrap(await api.POST("/settings/mcp-servers", { body }));
  }, "MCP server saved");

  function edit(s: Server) {
    setTools(null);
    setEditing({ id: s.id, name: s.name, transport: s.transport, url: s.url ?? "", command: s.command ?? "", args: s.args.join(" "),
      headers: Object.keys(s.headers).map((name) => ({ name, mode: "keep", value: s.headers[name].env ?? "" })), allowed: s.allowed_tools, enabled: s.enabled });
  }

  async function test(id: string) {
    try {
      const r = unwrap(await api.POST("/settings/mcp-servers/{id}/test", { params: { path: { id } } }));
      if (!r.ok) toast.error(r.error ?? "Connection failed");
      setTools(r.tools);
    } catch (e) {
      toast.error(errorMessage(e));
    }
  }

  return (
    <Section title="MCP servers" description="Read-only tools for the research agent and playground."
      actions={<Button onClick={() => { setTools(null); setEditing(NEW); }}>Add server</Button>}>
      <Alert>
        <AlertDescription>
          Only tools the server annotates as read-only and non-destructive can be enabled, and each must be allowlisted. Never connect a server that can post, message, or email.
        </AlertDescription>
      </Alert>
      <ul className="flex flex-col divide-y rounded-lg border">
        {data?.length === 0 && <li className="p-3 text-sm text-muted-foreground">No MCP servers.</li>}
        {data?.map((s) => (
          <li key={s.id} className="flex flex-col gap-2 p-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="text-sm">
              <p className="font-medium">{s.name} {!s.enabled && <Badge variant="destructive">Disabled</Badge>}</p>
              <p className="text-xs text-muted-foreground break-all">{s.transport === "stdio" ? `${s.command} ${s.args.join(" ")}` : s.url} · {s.allowed_tools.length} tools enabled</p>
            </div>
            <div className="flex gap-2">
              <Button variant="outline" size="sm" onClick={() => edit(s)}>Edit</Button>
              <Button variant="ghost" size="sm" onClick={() => confirm(`Remove ${s.name}?`) && remove.mutate(s.id)}>Remove</Button>
            </div>
          </li>
        ))}
      </ul>

      <Dialog open={editing !== null} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
          <DialogHeader><DialogTitle>{editing?.id ? `Edit ${editing.name}` : "Add MCP server"}</DialogTitle></DialogHeader>
          {editing && (
            <form className="flex flex-col gap-4" onSubmit={(e) => { e.preventDefault(); save.mutate(editing, { onSuccess: () => setEditing(null) }); }}>
              <div className="grid gap-4 sm:grid-cols-2">
                <Row id="mcp-name" label="Name"><Input id="mcp-name" required value={editing.name} onChange={(e) => setEditing({ ...editing, name: e.target.value })} /></Row>
                <Row id="mcp-t" label="Transport">
                  <NativeSelect id="mcp-t" value={editing.transport} onChange={(e) => setEditing({ ...editing, transport: e.target.value as Form["transport"] })}>
                    <option value="streamable_http">Streamable HTTP</option>
                    <option value="stdio">stdio (runs in the worker container)</option>
                  </NativeSelect>
                </Row>
              </div>
              {editing.transport === "streamable_http" ? (
                <Row id="mcp-url" label="URL"><Input id="mcp-url" type="url" required value={editing.url} onChange={(e) => setEditing({ ...editing, url: e.target.value })} /></Row>
              ) : (
                <div className="grid gap-4 sm:grid-cols-2">
                  <Row id="mcp-cmd" label="Command"><Input id="mcp-cmd" required value={editing.command} onChange={(e) => setEditing({ ...editing, command: e.target.value })} /></Row>
                  <Row id="mcp-args" label="Arguments" hint="Space-separated."><Input id="mcp-args" value={editing.args} onChange={(e) => setEditing({ ...editing, args: e.target.value })} /></Row>
                </div>
              )}
              <Row id="mcp-h" label="Headers" hint="Values are stored encrypted, or read from an env var.">
                <div className="flex flex-col gap-2">
                  {editing.headers.map((h, i) => (
                    <div key={i} className="flex flex-wrap gap-2">
                      <Input aria-label="Header name" className="w-40" value={h.name} onChange={(e) => { const hs = [...editing.headers]; hs[i] = { ...h, name: e.target.value }; setEditing({ ...editing, headers: hs }); }} />
                      <NativeSelect aria-label="Source" className="w-36" value={h.mode} onChange={(e) => { const hs = [...editing.headers]; hs[i] = { ...h, mode: e.target.value as HeaderRow["mode"], value: "" }; setEditing({ ...editing, headers: hs }); }}>
                        {h.mode === "keep" && <option value="keep">Keep current</option>}
                        <option value="value">Value</option>
                        <option value="env">Env var</option>
                      </NativeSelect>
                      {h.mode !== "keep" && (
                        <Input aria-label="Header value" type={h.mode === "value" ? "password" : "text"} className="min-w-0 flex-1" value={h.value} onChange={(e) => { const hs = [...editing.headers]; hs[i] = { ...h, value: e.target.value }; setEditing({ ...editing, headers: hs }); }} />
                      )}
                    </div>
                  ))}
                  <Button type="button" variant="outline" size="sm" className="self-start" onClick={() => setEditing({ ...editing, headers: [...editing.headers, { name: "Authorization", mode: "value", value: "" }] })}>Add header</Button>
                </div>
              </Row>
              <Check id="mcp-en" label="Enabled" checked={editing.enabled} onChange={(v) => setEditing({ ...editing, enabled: v })} />
              <fieldset className="flex flex-col gap-2 rounded-lg border p-3">
                <legend className="px-1 text-sm font-medium">Tools</legend>
                {editing.id ? (
                  <Button type="button" variant="outline" size="sm" className="self-start" onClick={() => test(editing.id!)}>Connect and list tools</Button>
                ) : (
                  <p className="text-xs text-muted-foreground">Save the server first, then list its tools to enable them.</p>
                )}
                {tools?.map((t) => (
                  <Check key={t.name} id={`tool-${t.name}`} disabled={!t.allowable}
                    label={`${t.name}${t.allowable ? "" : t.read_only ? " (destructive)" : " (not read-only)"}`}
                    hint={t.description}
                    checked={editing.allowed.includes(t.name)}
                    onChange={(v) => setEditing({ ...editing, allowed: v ? [...editing.allowed, t.name] : editing.allowed.filter((x) => x !== t.name) })} />
                ))}
                {!tools && editing.allowed.length > 0 && <p className="text-xs text-muted-foreground">Enabled: {editing.allowed.join(", ")}</p>}
              </fieldset>
              <DialogFooter><Button type="submit" disabled={save.isPending}>{save.isPending ? "Verifying…" : "Save"}</Button></DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </Section>
  );
}
