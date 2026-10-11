"use client";

import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { Row, Section } from "@/components/settings/common";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, errorMessage, unwrap } from "@/lib/api/client";
import { useSession } from "@/lib/session";

/** The signed-in person's own account: details and password. */
export default function AccountPage() {
  const { session, workspace } = useSession();
  const [f, setF] = useState({ current: "", next: "", confirm: "" });
  const change = useMutation({
    mutationFn: async () => {
      if (f.next !== f.confirm) throw new Error("The new passwords don't match.");
      return unwrap(await api.POST("/auth/password", { body: { current_password: f.current, new_password: f.next } }));
    },
    onSuccess: () => { setF({ current: "", next: "", confirm: "" }); toast.success("Password changed. Other sessions were signed out."); },
    onError: (e) => toast.error(errorMessage(e)),
  });
  return (
    <div className="mx-auto flex max-w-xl flex-col gap-8">
      <header>
        <h1 className="text-xl font-semibold">Account</h1>
        <p className="text-sm text-muted-foreground">{session.user.name} · {session.user.email} · {workspace.role} in {workspace.name}</p>
      </header>
      <Section title="Change password" description="Changing it signs you out everywhere except this browser.">
        <form className="flex flex-col gap-3" onSubmit={(e) => { e.preventDefault(); change.mutate(); }}>
          <Row id="pw-cur" label="Current password"><Input id="pw-cur" type="password" required autoComplete="current-password" value={f.current} onChange={(e) => setF({ ...f, current: e.target.value })} /></Row>
          <Row id="pw-new" label="New password" hint="At least 12 characters."><Input id="pw-new" type="password" required minLength={12} autoComplete="new-password" value={f.next} onChange={(e) => setF({ ...f, next: e.target.value })} /></Row>
          <Row id="pw-conf" label="Confirm new password"><Input id="pw-conf" type="password" required minLength={12} autoComplete="new-password" value={f.confirm} onChange={(e) => setF({ ...f, confirm: e.target.value })} /></Row>
          <div><Button type="submit" size="sm" disabled={change.isPending}>{change.isPending ? "Changing…" : "Change password"}</Button></div>
        </form>
      </Section>
    </div>
  );
}
