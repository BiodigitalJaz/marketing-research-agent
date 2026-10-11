"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { api, errorMessage, unwrap, type Schemas } from "@/lib/api/client";
import { ME_KEY, useSession } from "@/lib/session";

import { Row, Section } from "./common";

type Member = Schemas["Member"];
type Role = Member["role"];
const KEY = ["settings", "users"] as const;
const ROLES: { value: Role; label: string; hint: string }[] = [
  { value: "viewer", label: "Viewer", hint: "Reads the inbox, research, reports, and brand." },
  { value: "editor", label: "Editor", hint: "Also triages, drafts, runs research, and edits the brand and monitors." },
  { value: "admin", label: "Admin", hint: "Also manages sources, settings, and users." },
];

/** Workspace name and the people in it. The app never sends email: share temporary passwords yourself. */
export function UsersPanel() {
  return (
    <div className="flex flex-col gap-8">
      <WorkspaceName />
      <Members />
    </div>
  );
}

function WorkspaceName() {
  const qc = useQueryClient();
  const { workspace } = useSession();
  const [name, setName] = useState(workspace.name);
  const rename = useMutation({
    mutationFn: async () => unwrap(await api.PUT("/settings/workspace/name", { body: { name } })),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ME_KEY }); toast.success("Workspace renamed"); },
    onError: (e) => toast.error(errorMessage(e)),
  });
  return (
    <Section title="Workspace" description="Prompts call your brand by this name, so use the name you go by publicly.">
      <form className="flex max-w-md items-end gap-2" onSubmit={(e) => { e.preventDefault(); rename.mutate(); }}>
        <Row id="ws-name" label="Name" className="flex-1"><Input id="ws-name" required maxLength={200} value={name} onChange={(e) => setName(e.target.value)} /></Row>
        <Button type="submit" size="sm" disabled={rename.isPending || name.trim() === workspace.name}>Rename</Button>
      </form>
    </Section>
  );
}

function Members() {
  const qc = useQueryClient();
  const { data } = useQuery({ queryKey: KEY, queryFn: async () => unwrap(await api.GET("/settings/users")).items });
  const [adding, setAdding] = useState(false);
  const [resetting, setResetting] = useState<string | null>(null);
  const refresh = () => void qc.invalidateQueries({ queryKey: KEY });

  const update = useMutation({
    mutationFn: async ({ id, role }: { id: string; role: Role }) => unwrap(await api.PUT("/settings/users/{id}", { params: { path: { id } }, body: { role } })),
    onSuccess: () => { refresh(); toast.success("Role updated"); },
    onError: (e) => toast.error(errorMessage(e)),
  });
  const remove = useMutation({
    mutationFn: async (id: string) => unwrap(await api.DELETE("/settings/users/{id}", { params: { path: { id } } })),
    onSuccess: () => { refresh(); toast.success("Removed from the workspace"); },
    onError: (e) => toast.error(errorMessage(e)),
  });

  return (
    <Section title="Users" description="Everyone who can sign in to this workspace. Adding someone creates their account with a temporary password you share with them directly; they can change it under Account."
      actions={!adding && <Button size="sm" variant="outline" onClick={() => setAdding(true)}>Add a person</Button>}>
      {adding && <AddMember onDone={() => { setAdding(false); refresh(); }} />}
      <ul className="flex flex-col divide-y rounded-lg border">
        {data?.map((m) => (
          <li key={m.id} className="flex flex-col gap-2 p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <p className="font-medium">{m.name} {m.you && <Badge variant="outline">You</Badge>}</p>
                <p className="text-xs text-muted-foreground">{m.email} · added {new Date(m.created_at).toLocaleDateString()}</p>
              </div>
              <div className="flex items-center gap-2">
                <NativeSelect aria-label={`Role for ${m.email}`} className="w-28" value={m.role} disabled={update.isPending}
                  onChange={(e) => update.mutate({ id: m.id, role: e.target.value as Role })}>
                  {ROLES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
                </NativeSelect>
                {!m.you && <Button size="sm" variant="ghost" onClick={() => setResetting(resetting === m.id ? null : m.id)}>Reset password</Button>}
                {!m.you && <Button size="sm" variant="ghost" onClick={() => { if (confirm(`Remove ${m.email} from this workspace?`)) remove.mutate(m.id); }}>Remove</Button>}
              </div>
            </div>
            {resetting === m.id && <ResetPassword member={m} onDone={() => setResetting(null)} />}
          </li>
        ))}
      </ul>
      <ul className="flex flex-col gap-1 text-xs text-muted-foreground">
        {ROLES.map((r) => <li key={r.value}><span className="font-medium text-foreground">{r.label}:</span> {r.hint}</li>)}
      </ul>
    </Section>
  );
}

function AddMember({ onDone }: { onDone: () => void }) {
  const [f, setF] = useState({ email: "", name: "", role: "editor" as Role, password: "" });
  const add = useMutation({
    mutationFn: async () => unwrap(await api.POST("/settings/users", { body: f })),
    onSuccess: (res) => {
      if (res.kept_password) toast.success(`${res.member.email} added. They already have an account, so they sign in with their existing password.`);
      else toast.success(`${res.member.email} added. Share the temporary password with them yourself.`);
      onDone();
    },
    onError: (e) => toast.error(errorMessage(e)),
  });
  return (
    <form className="flex flex-col gap-3 rounded-lg border p-4" onSubmit={(e) => { e.preventDefault(); add.mutate(); }}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Row id="u-email" label="Email"><Input id="u-email" type="email" required value={f.email} onChange={(e) => setF({ ...f, email: e.target.value })} /></Row>
        <Row id="u-name" label="Name"><Input id="u-name" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Row>
        <Row id="u-role" label="Role">
          <NativeSelect id="u-role" value={f.role} onChange={(e) => setF({ ...f, role: e.target.value as Role })}>
            {ROLES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
          </NativeSelect>
        </Row>
        <Row id="u-pw" label="Temporary password" hint="At least 12 characters. Not needed for someone who already has an account in another workspace.">
          <Input id="u-pw" type="text" autoComplete="off" minLength={12} value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} />
        </Row>
      </div>
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={add.isPending}>{add.isPending ? "Adding…" : "Add"}</Button>
        <Button type="button" size="sm" variant="ghost" onClick={onDone}>Cancel</Button>
      </div>
    </form>
  );
}

function ResetPassword({ member, onDone }: { member: Member; onDone: () => void }) {
  const [password, setPassword] = useState("");
  const reset = useMutation({
    mutationFn: async () => unwrap(await api.POST("/settings/users/{id}/password", { params: { path: { id: member.id } }, body: { password } })),
    onSuccess: () => { toast.success(`Password reset; ${member.email} was signed out. Share the new password with them yourself.`); onDone(); },
    onError: (e) => toast.error(errorMessage(e)),
  });
  return (
    <form className="flex flex-wrap items-end gap-2 rounded-md bg-muted/40 p-3" onSubmit={(e) => { e.preventDefault(); reset.mutate(); }}>
      <Row id={`rp-${member.id}`} label="New temporary password" hint="At least 12 characters. Signs them out everywhere." className="min-w-64 flex-1">
        <Input id={`rp-${member.id}`} type="text" autoComplete="off" required minLength={12} value={password} onChange={(e) => setPassword(e.target.value)} />
      </Row>
      <Button type="submit" size="sm" disabled={reset.isPending}>Reset</Button>
      <Button type="button" size="sm" variant="ghost" onClick={onDone}>Cancel</Button>
    </form>
  );
}
