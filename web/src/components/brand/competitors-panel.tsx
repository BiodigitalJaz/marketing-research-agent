"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { ListInput } from "@/components/list-input";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { api, unwrap, type Schemas } from "@/lib/api/client";
import { useSession } from "@/lib/session";

import { Field } from "./field";
import { useCrudMutation } from "./use-crud";

type CompetitorInput = Schemas["CompetitorInput"];
const KEY = ["competitors"] as const;
const EMPTY: CompetitorInput = { name: "", domains: [], notes: null };

export function CompetitorsPanel() {
  const { canEdit } = useSession();
  const [editing, setEditing] = useState<{ id?: string; value: CompetitorInput } | null>(null);
  const { data, isPending } = useQuery({ queryKey: KEY, queryFn: async () => unwrap(await api.GET("/competitors")).items });
  const save = useCrudMutation(
    KEY,
    async ({ id, value }: { id?: string; value: CompetitorInput }) =>
      id
        ? unwrap(await api.PUT("/competitors/{id}", { params: { path: { id } }, body: value }))
        : unwrap(await api.POST("/competitors", { body: value })),
    "Competitor saved",
  );
  const remove = useCrudMutation(KEY, async (id: string) => unwrap(await api.DELETE("/competitors/{id}", { params: { path: { id } } })), "Competitor deleted");

  return (
    <section className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">Tracked for competitor complaints and share of voice.</p>
        {canEdit && <Button onClick={() => setEditing({ value: EMPTY })}>Add competitor</Button>}
      </div>
      {isPending ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : data?.length === 0 ? (
        <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">No competitors yet.</p>
      ) : (
        <ul className="flex flex-col divide-y rounded-lg border">
          {data?.map((c) => (
            <li key={c.id} className="flex flex-col gap-1 p-4 sm:flex-row sm:items-start sm:justify-between">
              <div className="min-w-0">
                <p className="font-medium">{c.name}</p>
                {c.domains.length > 0 && <p className="text-xs text-muted-foreground">{c.domains.join(", ")}</p>}
                {c.notes && <p className="mt-1 text-sm text-muted-foreground">{c.notes}</p>}
              </div>
              {canEdit && (
                <div className="flex shrink-0 gap-2">
                  <Button variant="outline" size="sm" onClick={() => setEditing({ id: c.id, value: { name: c.name, domains: c.domains, notes: c.notes ?? null } })}>
                    Edit
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => confirm(`Delete "${c.name}"?`) && remove.mutate(c.id)}>
                    Delete
                  </Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
      <Dialog open={editing !== null} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{editing?.id ? "Edit competitor" : "Add competitor"}</DialogTitle>
          </DialogHeader>
          {editing && (
            <form
              className="flex flex-col gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                save.mutate(editing, { onSuccess: () => setEditing(null) });
              }}
            >
              <Field id="c-name" label="Name">
                <Input id="c-name" required maxLength={200} value={editing.value.name} onChange={(e) => setEditing({ ...editing, value: { ...editing.value, name: e.target.value } })} />
              </Field>
              <Field id="c-domains" label="Domains">
                <ListInput id="c-domains" value={editing.value.domains} onChange={(l) => setEditing({ ...editing, value: { ...editing.value, domains: l } })} placeholder="example.com" />
              </Field>
              <Field id="c-notes" label="Notes">
                <Textarea id="c-notes" rows={3} value={editing.value.notes ?? ""} onChange={(e) => setEditing({ ...editing, value: { ...editing.value, notes: e.target.value || null } })} />
              </Field>
              <DialogFooter>
                <Button type="submit" disabled={save.isPending}>
                  {save.isPending ? "Saving…" : "Save"}
                </Button>
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </section>
  );
}
