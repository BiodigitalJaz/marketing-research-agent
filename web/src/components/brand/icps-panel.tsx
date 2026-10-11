"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { ListInput } from "@/components/list-input";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { api, unwrap, type Schemas } from "@/lib/api/client";
import { useSession } from "@/lib/session";

import { EmbeddedBadge, Field } from "./field";
import { useCrudMutation } from "./use-crud";

type ICPInput = Schemas["ICPInput"];
type Def = ICPInput["definition"];
const KEY = ["icps"] as const;
const EMPTY: ICPInput = { name: "", definition: { industries: [], company_sizes: [], roles: [], pain_points: [], buying_signals: [] } };
const FIELDS: { key: keyof Def; label: string }[] = [
  { key: "roles", label: "Roles" },
  { key: "pain_points", label: "Pain points" },
  { key: "buying_signals", label: "Buying signals" },
  { key: "industries", label: "Industries" },
  { key: "company_sizes", label: "Company / account sizes" },
];

export function ICPsPanel() {
  const { canEdit } = useSession();
  const [editing, setEditing] = useState<{ id?: string; value: ICPInput } | null>(null);
  const { data, isPending } = useQuery({
    queryKey: KEY,
    queryFn: async () => unwrap(await api.GET("/icps")).items,
    refetchInterval: (q) => (q.state.data?.some((i) => !i.embedded) ? 5000 : false),
  });
  const save = useCrudMutation(
    KEY,
    async ({ id, value }: { id?: string; value: ICPInput }) =>
      id
        ? unwrap(await api.PUT("/icps/{id}", { params: { path: { id } }, body: value }))
        : unwrap(await api.POST("/icps", { body: value })),
    "ICP saved",
  );
  const remove = useCrudMutation(KEY, async (id: string) => unwrap(await api.DELETE("/icps/{id}", { params: { path: { id } } })), "ICP deleted");

  return (
    <section className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">Who your brand is for. Used to judge relevance and fit.</p>
        {canEdit && <Button onClick={() => setEditing({ value: EMPTY })}>Add ICP</Button>}
      </div>
      {isPending ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : data?.length === 0 ? (
        <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">No ICPs yet.</p>
      ) : (
        <ul className="grid gap-3 md:grid-cols-2">
          {data?.map((icp) => (
            <li key={icp.id} className="flex flex-col gap-2 rounded-lg border p-4">
              <div className="flex items-start justify-between gap-2">
                <div>
                  <p className="font-medium">{icp.name}</p>
                  <EmbeddedBadge embedded={icp.embedded} />
                </div>
                {canEdit && (
                  <div className="flex gap-1">
                    <Button variant="outline" size="sm" onClick={() => setEditing({ id: icp.id, value: { name: icp.name, definition: icp.definition } })}>
                      Edit
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => confirm(`Delete "${icp.name}"?`) && remove.mutate(icp.id)}>
                      Delete
                    </Button>
                  </div>
                )}
              </div>
              {icp.definition.pain_points.length > 0 && (
                <ul className="list-disc pl-5 text-sm text-muted-foreground">
                  {icp.definition.pain_points.slice(0, 4).map((p) => (
                    <li key={p}>{p}</li>
                  ))}
                </ul>
              )}
            </li>
          ))}
        </ul>
      )}
      <Dialog open={editing !== null} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>{editing?.id ? "Edit ICP" : "Add ICP"}</DialogTitle>
          </DialogHeader>
          {editing && (
            <form
              className="flex flex-col gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                save.mutate(editing, { onSuccess: () => setEditing(null) });
              }}
            >
              <Field id="icp-name" label="Name">
                <Input id="icp-name" required maxLength={200} value={editing.value.name} onChange={(e) => setEditing({ ...editing, value: { ...editing.value, name: e.target.value } })} />
              </Field>
              {FIELDS.map((f) => (
                <Field key={f.key} id={`icp-${f.key}`} label={f.label}>
                  <ListInput
                    id={`icp-${f.key}`}
                    value={editing.value.definition[f.key]}
                    onChange={(l) => setEditing({ ...editing, value: { ...editing.value, definition: { ...editing.value.definition, [f.key]: l } } })}
                  />
                </Field>
              ))}
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
