"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, unwrap, type Schemas } from "@/lib/api/client";
import { cn } from "@/lib/utils";

type Detail = Schemas["OpportunityDetail"];
type Draft = Schemas["Draft"];
type Kind = Draft["kind"];

export const KIND_LABEL: Record<Kind, string> = { reply: "Reply", pitch: "Pitch", comment: "Comment" };
const VARIANT_LABEL: Record<string, string> = {
  helpful: "Helpful", story: "Story", short: "Short", pitch: "Pitch", perspective: "Perspective", brief: "Brief",
};

/** Draft variants for an opportunity. Drafts are suggestions to copy; there is deliberately no way to post from here. */
export function DraftsPanel({ opp, canEdit }: { opp: Detail; canEdit: boolean }) {
  const qc = useQueryClient();
  const pending = opp.drafting.state === "pending";
  const kinds = [...new Set(opp.drafts.map((d) => d.kind))];
  const [kind, setKind] = useState<Kind>((opp.draft_kind as Kind) || "reply");
  const [instruction, setInstruction] = useState("");

  const request = useMutation({
    mutationFn: async (body: { kind?: Kind; instruction?: string }) =>
      unwrap(await api.POST("/opportunities/{id}/drafts", { params: { path: { id: opp.id } }, body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["opportunity", opp.id] }),
    onError: (e) => toast.error(errorMessage(e)),
  });

  // Drafts are generated lazily the first time an editor opens the opportunity.
  const asked = useRef(false);
  useEffect(() => {
    if (canEdit && !asked.current && opp.drafts.length === 0 && opp.drafting.state === "idle") {
      asked.current = true;
      request.mutate({});
    }
  }, [canEdit, opp.drafts.length, opp.drafting.state, request]);

  const shown = opp.drafts.filter((d) => d.kind === (kinds.includes(kind) ? kind : kinds[0]));
  return (
    <section className="flex flex-col gap-3" aria-labelledby="drafts-heading">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="drafts-heading" className="font-semibold">Drafts</h2>
        {kinds.length > 1 && (
          <Tabs value={shown[0]?.kind ?? kind} onValueChange={(v) => setKind(v as Kind)}>
            <TabsList>{kinds.map((k) => <TabsTrigger key={k} value={k}>{KIND_LABEL[k]}</TabsTrigger>)}</TabsList>
          </Tabs>
        )}
      </div>

      {pending && (
        <p role="status" className="flex items-center gap-2 rounded-lg border border-dashed p-3 text-sm text-muted-foreground">
          <span className="size-2 animate-pulse rounded-full bg-primary" aria-hidden />
          Drafting… this usually takes under a minute.
          {opp.drafting.error && <span className="text-xs">Retrying after: {opp.drafting.error}</span>}
        </p>
      )}
      {opp.drafting.state === "failed" && (
        <div role="alert" className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-destructive/40 p-3 text-sm text-destructive">
          <span>Drafting failed{opp.drafting.error ? `: ${opp.drafting.error}` : "."}</span>
          {canEdit && <Button size="sm" variant="outline" onClick={() => request.mutate({ kind })}>Try again</Button>}
        </div>
      )}
      {!pending && opp.drafts.length === 0 && opp.drafting.state === "idle" && !canEdit && (
        <p className="text-sm text-muted-foreground">No drafts yet. An editor can generate them.</p>
      )}

      {shown.length > 0 && <DraftVariants key={shown[0].id} oppId={opp.id} drafts={shown} />}

      {canEdit && (
        <form
          className="flex flex-col gap-2 rounded-lg border p-3"
          onSubmit={(e) => {
            e.preventDefault();
            request.mutate({ kind, instruction: instruction.trim() || undefined }, { onSuccess: () => setInstruction("") });
          }}
        >
          <Label htmlFor="regen-instruction">Regenerate with instruction</Label>
          <Textarea id="regen-instruction" maxLength={1000} rows={2} placeholder="e.g. Shorter, and mention approval mode only if it answers the question"
            value={instruction} onChange={(e) => setInstruction(e.target.value)} />
          <div className="flex flex-wrap items-center gap-2">
            <NativeSelect aria-label="Draft kind" className="w-36" value={kind} onChange={(e) => setKind(e.target.value as Kind)}>
              {(Object.keys(KIND_LABEL) as Kind[]).map((k) => <option key={k} value={k}>{KIND_LABEL[k]}</option>)}
            </NativeSelect>
            <Button type="submit" variant="outline" disabled={pending || request.isPending}>
              {opp.drafts.length ? "Regenerate" : "Generate"}
            </Button>
          </div>
        </form>
      )}
    </section>
  );
}

function DraftVariants({ oppId, drafts }: { oppId: string; drafts: Draft[] }) {
  const [tab, setTab] = useState(drafts[0].id);
  const instruction = drafts[0].instruction;
  return (
    <Tabs value={tab} onValueChange={setTab} className="gap-2">
      <TabsList>
        {drafts.map((d) => (
          <TabsTrigger key={d.id} value={d.id}>
            {VARIANT_LABEL[d.variant] ?? d.variant}
            {d.rule_flags.some((f) => f.severity === "block") && <span className="ml-1 size-1.5 rounded-full bg-destructive" aria-label="has blocking issues" />}
          </TabsTrigger>
        ))}
      </TabsList>
      {instruction && <p className="text-xs text-muted-foreground">Generated with instruction: “{instruction}”</p>}
      {drafts.map((d) => <TabsContent key={d.id} value={d.id}><DraftCard oppId={oppId} draft={d} /></TabsContent>)}
    </Tabs>
  );
}

const SEVERITY_STYLE: Record<string, string> = {
  block: "border-destructive/40 bg-destructive/5 text-destructive",
  warning: "border-amber-500/40 bg-amber-50 text-amber-900 dark:bg-amber-950 dark:text-amber-100",
  info: "border-border bg-muted/50 text-muted-foreground",
};

function DraftCard({ oppId, draft: d }: { oppId: string; draft: Draft }) {
  const blocked = d.rule_flags.some((f) => f.severity === "block");
  const over = d.word_count > d.word_limit;
  const link = d.include_link ? d.portfolio_items.find((p) => p.url)?.url : undefined;

  async function copy(text: string, what: string) {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(`${what} copied`);
      void api.POST("/opportunities/{id}/events", { params: { path: { id: oppId } }, body: { event: "copied_draft", meta: { draft_id: d.id, variant: d.variant, part: what.toLowerCase() } } });
    } catch {
      toast.error("Copy failed; select the text instead.");
    }
  }

  return (
    <article className="flex flex-col gap-3 rounded-lg border p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Badge variant="outline">AI draft — review before posting</Badge>
        <span className={cn("text-xs tabular-nums", over ? "text-destructive" : "text-muted-foreground")}>{d.word_count} / {d.word_limit} words</span>
      </div>

      {d.rule_flags.length > 0 && (
        <ul className="flex flex-col gap-1.5" aria-label="Checks">
          {d.rule_flags.map((f, i) => (
            <li key={i} className={cn("rounded-md border px-2.5 py-1.5 text-xs", SEVERITY_STYLE[f.severity])}>
              <span className="font-medium">{f.severity === "block" ? "Do not post as written" : f.severity === "warning" ? "Check" : "Note"}:</span> {f.note}
            </li>
          ))}
        </ul>
      )}

      {d.subject && (
        <div className="flex items-start justify-between gap-2">
          <p className="text-sm"><span className="text-muted-foreground">Subject:</span> <span className="font-medium">{d.subject}</span></p>
          <Button size="sm" variant="ghost" onClick={() => void copy(d.subject!, "Subject")}>Copy subject</Button>
        </div>
      )}
      <p className="text-sm leading-relaxed whitespace-pre-wrap">{d.body}</p>

      {d.portfolio_items.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
          <span>Portfolio:</span>
          {d.portfolio_items.map((p) => (
            p.url ? <a key={p.id} href={p.url} target="_blank" rel="noreferrer noopener" className="underline underline-offset-2">{p.title}</a> : <span key={p.id}>{p.title}</span>
          ))}
        </div>
      )}
      {link && !d.body.includes(link) && <p className="text-xs text-muted-foreground">Suggested link to add: <span className="break-all">{link}</span></p>}

      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant={blocked ? "outline" : "default"} onClick={() => void copy(d.body, "Draft")}>{blocked ? "Copy anyway" : "Copy draft"}</Button>
      </div>
    </article>
  );
}
