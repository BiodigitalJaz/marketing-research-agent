"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { useCrudMutation } from "@/components/brand/use-crud";
import { Check, num, Row, Section, Status } from "@/components/settings/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, unwrap, type Schemas } from "@/lib/api/client";
import { useSession } from "@/lib/session";

type Source = Schemas["Source"];
type Preview = Schemas["ItemPreview"];
export const SOURCES_KEY = ["sources"] as const;

type Kind = "rss" | "alerts" | "hn" | "search";
type Form = { id?: string; kind: Kind; name: string; enabled: boolean; cfg: Record<string, unknown> };

const KIND_LABEL: Record<Kind, string> = { alerts: "Google Alert (RSS)", rss: "RSS / Atom feed", hn: "Hacker News", search: "Web search" };
const CONNECTOR: Record<Kind, Source["connector"]> = { alerts: "rss", rss: "rss", hn: "hn", search: "search" };

function kindOf(s: Source): Kind {
  if (s.connector === "rss") return String((s.config as { feed_url?: string }).feed_url ?? "").includes("google.com/alerts") ? "alerts" : "rss";
  return s.connector;
}

const DEFAULTS: Record<Kind, Record<string, unknown>> = {
  alerts: { feed_url: "", match_all: true, full_text: true },
  rss: { feed_url: "", match_all: false, full_text: true },
  hn: { lists: ["newstories", "askstories"], min_score: 0 },
  search: { max_queries: 5, max_results: 10, full_text: true },
};

export function SourcesPanel() {
  const { isAdmin } = useSession();
  const { data } = useQuery({ queryKey: SOURCES_KEY, queryFn: async () => unwrap(await api.GET("/sources")).items });
  const [editing, setEditing] = useState<Form | null>(null);
  const [preview, setPreview] = useState<{ name: string; ok: boolean; error?: string; items: Preview[] } | null>(null);
  const [testing, setTesting] = useState<string | null>(null);

  const save = useCrudMutation(SOURCES_KEY, async (f: Form) => {
    const body = { connector: CONNECTOR[f.kind], name: f.name, enabled: f.enabled, config: f.cfg };
    return f.id ? unwrap(await api.PUT("/sources/{id}", { params: { path: { id: f.id } }, body })) : unwrap(await api.POST("/sources", { body }));
  }, "Source saved");
  const remove = useCrudMutation(SOURCES_KEY, async (id: string) => unwrap(await api.DELETE("/sources/{id}", { params: { path: { id } } })), "Source deleted");

  async function test(s: Source) {
    setTesting(s.id);
    try {
      const r = unwrap(await api.POST("/sources/{id}/test", { params: { path: { id: s.id } } }));
      setPreview({ name: s.name, ok: r.ok, error: r.error, items: r.items });
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setTesting(null);
    }
  }

  return (
    <Section title="Sources" description="Where content comes from. All sources are free and read-only."
      actions={isAdmin && (
        <div className="flex flex-wrap gap-2">
          {(["alerts", "rss", "hn", "search"] as Kind[]).map((k) => (
            <Button key={k} size="sm" variant={k === "alerts" ? "default" : "outline"} onClick={() => setEditing({ kind: k, name: "", enabled: true, cfg: { ...DEFAULTS[k] } })}>
              Add {KIND_LABEL[k]}
            </Button>
          ))}
        </div>
      )}>
      <ul className="flex flex-col divide-y rounded-lg border">
        {data?.length === 0 && <li className="p-3 text-sm text-muted-foreground">No sources yet.</li>}
        {data?.map((s) => (
          <li key={s.id} className="flex flex-col gap-2 p-3 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0 text-sm">
              <p className="flex flex-wrap items-center gap-2 font-medium">
                {s.name}
                <Badge variant="outline">{KIND_LABEL[kindOf(s)]}</Badge>
                {!s.enabled && <Badge variant="secondary">Paused</Badge>}
                <Status ok={s.health === "ok" || s.health === "unknown"}>{s.health === "unknown" ? "not run yet" : s.health}</Status>
              </p>
              <p className="text-xs text-muted-foreground">
                {s.item_count} items collected{s.last_run_at && ` · last run ${new Date(s.last_run_at).toLocaleString()}`}
              </p>
              {s.last_error && <p className="text-xs break-words text-destructive">{s.last_error}</p>}
            </div>
            {isAdmin && (
              <div className="flex shrink-0 gap-2">
                <Button variant="outline" size="sm" disabled={testing === s.id} onClick={() => test(s)}>{testing === s.id ? "Testing…" : "Test"}</Button>
                <Button variant="outline" size="sm" onClick={() => setEditing({ id: s.id, kind: kindOf(s), name: s.name, enabled: s.enabled, cfg: { ...(s.config as Record<string, unknown>) } })}>Edit</Button>
                <Button variant="ghost" size="sm" onClick={() => confirm(`Delete ${s.name}? Collected items from it are deleted too.`) && remove.mutate(s.id)}>Delete</Button>
              </div>
            )}
          </li>
        ))}
      </ul>

      <Dialog open={editing !== null} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
          <DialogHeader><DialogTitle>{editing?.id ? `Edit ${editing.name}` : `Add ${editing ? KIND_LABEL[editing.kind] : ""}`}</DialogTitle></DialogHeader>
          {editing && (
            <form className="flex flex-col gap-4" onSubmit={(e) => { e.preventDefault(); save.mutate(editing, { onSuccess: () => setEditing(null) }); }}>
              <Row id="s-name" label="Name"><Input id="s-name" required value={editing.name} onChange={(e) => setEditing({ ...editing, name: e.target.value })} /></Row>
              <SourceFields form={editing} onChange={setEditing} />
              <RulesFields form={editing} onChange={setEditing} />
              <Check id="s-en" label="Enabled" checked={editing.enabled} onChange={(v) => setEditing({ ...editing, enabled: v })} />
              <DialogFooter><Button type="submit" disabled={save.isPending}>{save.isPending ? "Saving…" : "Save"}</Button></DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>

      <Dialog open={preview !== null} onOpenChange={(o) => !o && setPreview(null)}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>Test: {preview?.name}</DialogTitle>
            <DialogDescription>A one-off fetch matched against your tracked keywords. Nothing was saved.</DialogDescription>
          </DialogHeader>
          {preview?.error && <p className="text-sm text-destructive">{preview.error}</p>}
          {preview && preview.items.length === 0 && !preview.error && <p className="text-sm text-muted-foreground">No matching items in the recent window.</p>}
          <ul className="flex flex-col divide-y">
            {preview?.items.map((it) => (
              <li key={it.external_id} className="py-2 text-sm">
                <a href={it.url} target="_blank" rel="noreferrer noopener" className="font-medium underline-offset-2 hover:underline">{it.title || it.url}</a>
                <p className="text-xs text-muted-foreground">{it.community}{it.published_at && ` · ${new Date(it.published_at).toLocaleString()}`}</p>
                <p className="line-clamp-3 text-xs text-muted-foreground">{it.snippet}</p>
              </li>
            ))}
          </ul>
        </DialogContent>
      </Dialog>
    </Section>
  );
}

function SourceFields({ form: f, onChange }: { form: Form; onChange: (f: Form) => void }) {
  const set = (patch: Record<string, unknown>) => onChange({ ...f, cfg: { ...f.cfg, ...patch } });
  const c = f.cfg as Record<string, never>;
  switch (f.kind) {
    case "alerts":
    case "rss":
      return (
        <>
          {f.kind === "alerts" && (
            <ol className="list-decimal rounded-lg bg-muted/50 p-3 pl-7 text-xs text-muted-foreground">
              <li>Open google.com/alerts and enter a phrase you track (for example &quot;project management for agencies&quot;). To follow a community such as Reddit, add <code>site:reddit.com</code> to the phrase.</li>
              <li>Show options → Deliver to: <strong>RSS feed</strong> → Create alert.</li>
              <li>Copy the RSS icon&apos;s link and paste it below. One alert per phrase.</li>
            </ol>
          )}
          <Row id="s-url" label="Feed URL"><Input id="s-url" type="url" required value={c.feed_url ?? ""} onChange={(e) => set({ feed_url: e.target.value })} onBlur={(e) => set({ feed_url: e.target.value.trim() })} /></Row>
          <Check id="s-all" label="Keep every item (feed is already on-topic)" hint="Off: only items matching the monitor's query or tracked keywords are kept. Negative keywords always apply."
            checked={Boolean(c.match_all)} onChange={(v) => set({ match_all: v })} />
          <Check id="s-ft" label="Fetch the full article when the feed only has a snippet" hint="Polite fetch: respects robots.txt and rate limits." checked={Boolean(c.full_text)} onChange={(v) => set({ full_text: v })} />
        </>
      );
    case "hn": {
      const lists = (c.lists as string[] | undefined) ?? [];
      return (
        <>
          <fieldset className="flex flex-col gap-1.5">
            <legend className="text-sm font-medium">Lists</legend>
            {["newstories", "askstories", "showstories", "topstories"].map((l) => (
              <Check key={l} id={`hn-${l}`} label={l} checked={lists.includes(l)} onChange={(v) => set({ lists: v ? [...lists, l] : lists.filter((x) => x !== l) })} />
            ))}
          </fieldset>
          <Row id="hn-min" label="Minimum score"><Input id="hn-min" type="number" min={0} value={c.min_score ?? 0} onChange={(e) => set({ min_score: num(e.target.value) ?? 0 })} /></Row>
        </>
      );
    }
    case "search": {
      const sites = (c.site_include as string[] | undefined) ?? [];
      return (
        <>
          <p className="text-xs text-muted-foreground">Runs the monitor&apos;s query (or each tracked keyword) through the workspace search backend: SearXNG by default, set under Settings.</p>
          <div className="grid grid-cols-2 gap-3">
            <Row id="se-q" label="Queries per run"><Input id="se-q" type="number" min={1} max={25} value={c.max_queries ?? 5} onChange={(e) => set({ max_queries: num(e.target.value) })} /></Row>
            <Row id="se-r" label="Results per query"><Input id="se-r" type="number" min={1} max={50} value={c.max_results ?? 10} onChange={(e) => set({ max_results: num(e.target.value) })} /></Row>
          </div>
          <Row id="se-sites" label="Limit to sites" hint="Comma-separated hostnames, e.g. stackoverflow.com. Leave empty for all.">
            <Input id="se-sites" value={sites.join(", ")} onChange={(e) => set({ site_include: e.target.value.split(",").map((x) => x.trim()).filter(Boolean) })} />
          </Row>
          <Check id="se-all" label="Keep every result" hint="Off (recommended): results must match the monitor's query or tracked keywords in their title or snippet." checked={Boolean(c.match_all)} onChange={(v) => set({ match_all: v })} />
          <Check id="se-ft" label="Fetch result pages for full text" checked={Boolean(c.full_text)} onChange={(v) => set({ full_text: v })} />
        </>
      );
    }
  }
}

type Rules = { no_links?: boolean; no_self_promotion?: boolean; disclose_affiliation?: boolean; notes?: string[] };

/** Community posting rules (config.rules): drafting enforces them; collection ignores them. */
function RulesFields({ form: f, onChange }: { form: Form; onChange: (f: Form) => void }) {
  const rules = (f.cfg.rules as Rules | undefined) ?? {};
  const set = (patch: Rules) => onChange({ ...f, cfg: { ...f.cfg, rules: { ...rules, ...patch } } });
  return (
    <fieldset className="flex flex-col gap-2 rounded-lg border p-3">
      <legend className="px-1 text-sm font-medium">Community rules for drafts</legend>
      <Check id="r-links" label="No links" hint="Links and domain names are removed from drafts for this source." checked={Boolean(rules.no_links)} onChange={(v) => set({ no_links: v })} />
      <Check id="r-promo" label="No self-promotion" hint="Drafts that mention the brand are flagged." checked={Boolean(rules.no_self_promotion)} onChange={(v) => set({ no_self_promotion: v })} />
      <Check id="r-disclose" label="Affiliation must be disclosed" hint="A brand mention without disclosure is marked “do not post as written”." checked={Boolean(rules.disclose_affiliation)} onChange={(v) => set({ disclose_affiliation: v })} />
      <Row id="r-notes" label="Other rules" hint="One per line, in plain words. The model checks every draft against them.">
        <Textarea id="r-notes" rows={3} value={(rules.notes ?? []).join("\n")}
          onChange={(e) => set({ notes: e.target.value.split("\n").slice(0, 20) })}
          onBlur={(e) => set({ notes: e.target.value.split("\n").map((x) => x.trim()).filter(Boolean).slice(0, 20) })} />
      </Row>
    </fieldset>
  );
}

export function SourceSelect({ value, onChange, sources }: { value: string[]; onChange: (ids: string[]) => void; sources: Source[] }) {
  return (
    <fieldset className="flex flex-col gap-1.5">
      {sources.map((s) => (
        <Check key={s.id} id={`ms-${s.id}`} label={`${s.name} (${KIND_LABEL[kindOf(s)]})`} checked={value.includes(s.id)}
          onChange={(v) => onChange(v ? [...value, s.id] : value.filter((x) => x !== s.id))} />
      ))}
    </fieldset>
  );
}

export function KindBadge({ s }: { s: Source }) {
  return <Badge variant="outline">{KIND_LABEL[kindOf(s)]}</Badge>;
}

