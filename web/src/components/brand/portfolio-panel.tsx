"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { ListInput } from "@/components/list-input";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { api, unwrap, type Schemas } from "@/lib/api/client";
import { useSession } from "@/lib/session";

import { EmbeddedBadge, Field } from "./field";
import { useCrudMutation } from "./use-crud";

type Item = Schemas["PortfolioItem"];
type Input_ = Schemas["PortfolioInput"];
const KEY = ["portfolio"] as const;
const KINDS: Schemas["PortfolioKind"][] = ["blog_post", "case_study", "work_sample", "talk", "other"];
const KIND_LABEL: Record<Schemas["PortfolioKind"], string> = {
  blog_post: "Article / explainer",
  case_study: "Case study",
  work_sample: "Work sample",
  talk: "Talk",
  other: "Other",
};
const EMPTY: Input_ = { kind: "blog_post", title: "", url: null, summary: "", tags: { industries: [], services: [], problems: [] } };

export function PortfolioPanel() {
  const { canEdit } = useSession();
  const [editing, setEditing] = useState<{ id?: string; value: Input_ } | null>(null);
  const { data, isPending } = useQuery({
    queryKey: KEY,
    queryFn: async () => unwrap(await api.GET("/portfolio")).items,
    // Poll while any embedding is pending so the badge updates.
    refetchInterval: (q) => (q.state.data?.some((i) => !i.embedded) ? 5000 : false),
  });

  const save = useCrudMutation(
    KEY,
    async ({ id, value }: { id?: string; value: Input_ }) =>
      id
        ? unwrap(await api.PUT("/portfolio/{id}", { params: { path: { id } }, body: value }))
        : unwrap(await api.POST("/portfolio", { body: value })),
    "Portfolio item saved",
  );
  const remove = useCrudMutation(
    KEY,
    async (id: string) => unwrap(await api.DELETE("/portfolio/{id}", { params: { path: { id } } })),
    "Portfolio item deleted",
  );

  return (
    <section className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">Articles, explainers, and other pieces drafts can point to.</p>
        {canEdit && <Button onClick={() => setEditing({ value: EMPTY })}>Add item</Button>}
      </div>
      {isPending ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : data?.length === 0 ? (
        <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">No portfolio items yet.</p>
      ) : (
        <ul className="flex flex-col divide-y rounded-lg border">
          {data?.map((item: Item) => (
            <li key={item.id} className="flex flex-col gap-1 p-4 sm:flex-row sm:items-start sm:justify-between">
              <div className="min-w-0">
                <p className="font-medium break-words">{item.title}</p>
                <p className="text-xs text-muted-foreground">
                  {KIND_LABEL[item.kind]} · <EmbeddedBadge embedded={item.embedded} />
                  {item.url && (
                    <>
                      {" · "}
                      <a href={item.url} target="_blank" rel="noreferrer noopener" className="underline">
                        link
                      </a>
                    </>
                  )}
                </p>
                <p className="mt-1 line-clamp-2 text-sm text-muted-foreground">{item.summary}</p>
              </div>
              {canEdit && (
                <div className="flex shrink-0 gap-2">
                  <Button variant="outline" size="sm" onClick={() => setEditing({ id: item.id, value: { kind: item.kind, title: item.title, url: item.url ?? null, summary: item.summary, tags: item.tags } })}>
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => confirm(`Delete "${item.title}"?`) && remove.mutate(item.id)}
                  >
                    Delete
                  </Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}

      <Dialog open={editing !== null} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>{editing?.id ? "Edit portfolio item" : "Add portfolio item"}</DialogTitle>
          </DialogHeader>
          {editing && (
            <form
              className="flex flex-col gap-4"
              onSubmit={(e) => {
                e.preventDefault();
                save.mutate(editing, { onSuccess: () => setEditing(null) });
              }}
            >
              <PortfolioFields value={editing.value} onChange={(value) => setEditing({ ...editing, value })} />
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

function PortfolioFields({ value: v, onChange }: { value: Input_; onChange: (v: Input_) => void }) {
  const tags = (k: keyof Input_["tags"], list: string[]) => onChange({ ...v, tags: { ...v.tags, [k]: list } });
  return (
    <>
      <Field id="p-kind" label="Kind">
        <NativeSelect id="p-kind" value={v.kind} onChange={(e) => onChange({ ...v, kind: e.target.value as Input_["kind"] })}>
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {KIND_LABEL[k]}
            </option>
          ))}
        </NativeSelect>
      </Field>
      <Field id="p-title" label="Title">
        <Input id="p-title" required maxLength={300} value={v.title} onChange={(e) => onChange({ ...v, title: e.target.value })} />
      </Field>
      <Field id="p-url" label="URL" hint="Optional; leave empty until published.">
        <Input id="p-url" type="url" value={v.url ?? ""} onChange={(e) => onChange({ ...v, url: e.target.value || null })} />
      </Field>
      <Field id="p-summary" label="Summary" hint="What it covers. This text is embedded for matching.">
        <Textarea id="p-summary" required rows={4} maxLength={5000} value={v.summary} onChange={(e) => onChange({ ...v, summary: e.target.value })} />
      </Field>
      <Field id="p-problems" label="Problems addressed">
        <ListInput id="p-problems" value={v.tags.problems} onChange={(l) => tags("problems", l)} />
      </Field>
      <Field id="p-industries" label="Industries">
        <ListInput id="p-industries" value={v.tags.industries} onChange={(l) => tags("industries", l)} />
      </Field>
      <Field id="p-services" label="Services">
        <ListInput id="p-services" value={v.tags.services} onChange={(l) => tags("services", l)} />
      </Field>
    </>
  );
}
