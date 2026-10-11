"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { api, errorMessage, unwrap, type Schemas } from "@/lib/api/client";
import { OFF_IN_LOCAL_ONLY } from "@/lib/local-only";

import { Check, num, Row, Section } from "./common";

type WS = Schemas["WorkspaceSettings"];
const KEY = ["settings", "workspace"] as const;

export function WorkspacePanel() {
  const { data } = useQuery({ queryKey: KEY, queryFn: async () => unwrap(await api.GET("/settings/workspace")) });
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  return <WorkspaceForm key={data.updated_at ?? "new"} initial={data} />;
}

function WorkspaceForm({ initial }: { initial: WS }) {
  const qc = useQueryClient();
  const { data: providers } = useQuery({ queryKey: ["settings", "providers"], queryFn: async () => unwrap(await api.GET("/settings/providers")) });
  const [w, setW] = useState<WS>(initial);
  const [braveKey, setBraveKey] = useState("");
  const [googleKey, setGoogleKey] = useState("");
  const save = useMutation({
    mutationFn: async () => unwrap(await api.PUT("/settings/workspace", { body: { local_only: w.local_only, embeddings: w.embeddings, search: w.search, search_api_key: braveKey || undefined, google_pse_api_key: googleKey || undefined } })),
    onSuccess: (res) => { qc.setQueryData(KEY, res); void qc.invalidateQueries({ queryKey: ["settings"] }); toast.success("Settings saved"); setBraveKey(""); setGoogleKey(""); },
    onError: (e) => toast.error(errorMessage(e)),
  });
  const compat = providers?.items.filter((p) => p.kind === "openai_compat") ?? [];
  const [discovered, setDiscovered] = useState<string[] | null>(null);
  // Same discovery as Settings → Models; models whose id says "embed" first.
  const discover = useMutation({
    mutationFn: async (id: string) => unwrap(await api.GET("/settings/providers/{id}/discover", { params: { path: { id } } })).items.map((m) => m.id),
    onSuccess: (ids) => {
      const isEmbed = (id: string) => /embed/i.test(id);
      setDiscovered([...ids.filter(isEmbed), ...ids.filter((id) => !isEmbed(id))]);
    },
    onError: (e) => toast.error(errorMessage(e)),
  });
  const probe = useMutation({
    mutationFn: async (body: { provider_id: string; model: string }) => unwrap(await api.POST("/settings/embeddings/probe", { body })),
    onSuccess: (r) => { setW((cur) => ({ ...cur, embeddings: { ...cur.embeddings, dimensions: r.dimensions } })); toast.success(`${r.dimensions} dimensions`); },
    onError: (e) => toast.error(errorMessage(e)),
  });
  const embProvider = compat.find((p) => p.id === w.embeddings.provider_id);

  return (
    <form className="flex flex-col gap-8" onSubmit={(e) => { e.preventDefault(); save.mutate(); }}>
      <Section title="Local-only mode" description="When on, every model call goes to local providers only. Internet access is unaffected: collection and web search keep running.">
        <Check id="ws-local" label="Use local providers only" checked={w.local_only} onChange={(v) => setW({ ...w, local_only: v })}
          hint="Tasks whose primary and fallback are both hosted become unavailable until a local model is routed." />
        {w.local_only && embProvider && !embProvider.is_local && (
          <Alert variant="destructive"><AlertDescription>The embeddings provider is hosted. Choose a local one below to enable local-only mode.</AlertDescription></Alert>
        )}
      </Section>
      <Section title="Embeddings" description="Used for matching items to your portfolio and ICPs. Changing the model re-embeds everything in the background.">
        <div className="grid gap-4 sm:grid-cols-3">
          <Row id="emb-p" label="Provider">
            <NativeSelect id="emb-p" value={w.embeddings.provider_id ?? ""} onChange={(e) => setW({ ...w, embeddings: { ...w.embeddings, provider_id: e.target.value } })}>
              <option value="">Not configured</option>
              {compat.map((p) => <option key={p.id} value={p.id} disabled={w.local_only && !p.is_local}>{p.id}{p.is_local ? " (local)" : w.local_only ? OFF_IN_LOCAL_ONLY : ""}</option>)}
            </NativeSelect>
          </Row>
          <Row id="emb-m" label="Model" hint={discovered ? `${discovered.length} models found; embedding models listed first.` : "Type a model id, or discover what the provider serves."}>
            <div className="flex gap-2">
              <Input id="emb-m" list="emb-models" value={w.embeddings.model ?? ""} onChange={(e) => setW({ ...w, embeddings: { ...w.embeddings, model: e.target.value } })} />
              <Button type="button" size="sm" variant="outline" disabled={!w.embeddings.provider_id || discover.isPending} onClick={() => discover.mutate(w.embeddings.provider_id!)}>
                {discover.isPending ? "…" : "Discover"}
              </Button>
            </div>
            <datalist id="emb-models">{discovered?.map((m) => <option key={m} value={m} />)}</datalist>
          </Row>
          <Row id="emb-d" label="Dimensions" hint="Detect embeds a short text with the model and reads the vector size.">
            <div className="flex gap-2">
              <Input id="emb-d" type="number" min={1} value={w.embeddings.dimensions ?? ""} onChange={(e) => setW({ ...w, embeddings: { ...w.embeddings, dimensions: num(e.target.value) } })} />
              <Button type="button" size="sm" variant="outline" disabled={!w.embeddings.provider_id || !w.embeddings.model || probe.isPending}
                onClick={() => probe.mutate({ provider_id: w.embeddings.provider_id!, model: w.embeddings.model! })}>
                {probe.isPending ? "…" : "Detect"}
              </Button>
            </div>
          </Row>
        </div>
      </Section>
      <Section title="Web search" description="Used by the Web search source and, later, the research agent. SearXNG is self-hosted and free; Google and Brave are optional paid APIs.">
        <Row id="se-backend" label="Backend" className="max-w-sm">
          <NativeSelect id="se-backend" value={w.search.backend ?? ""} onChange={(e) => setW({ ...w, search: { ...w.search, backend: e.target.value as never } })}>
            <option value="">None</option>
            <option value="searxng">SearXNG (self-hosted, free)</option>
            <option value="google_pse">Google Programmable Search (free daily quota, then paid)</option>
            <option value="brave">Brave Search API (paid)</option>
          </NativeSelect>
        </Row>
        <p className="text-xs text-muted-foreground">Status: {w.search_key_set ? "configured" : "not configured"}</p>
        {w.search.backend === "searxng" && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Row id="sx-url" label="SearXNG URL" hint="In the cluster: http://searxng:8080"><Input id="sx-url" type="url" value={w.search.searxng_url ?? ""} onChange={(e) => setW({ ...w, search: { ...w.search, searxng_url: e.target.value } })} /></Row>
            <Row id="sx-cat" label="Categories" hint="Default: general,science,it,news"><Input id="sx-cat" value={w.search.searxng_categories ?? ""} onChange={(e) => setW({ ...w, search: { ...w.search, searxng_categories: e.target.value } })} /></Row>
          </div>
        )}
        {w.search.backend === "google_pse" && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Row id="gp-cx" label="Search engine ID (cx)"><Input id="gp-cx" value={w.search.google_pse_cx ?? ""} onChange={(e) => setW({ ...w, search: { ...w.search, google_pse_cx: e.target.value } })} /></Row>
            <Row id="gp-key" label="API key" hint={initial.encryption_enabled ? "Paste to set or replace; stored encrypted." : "Set RESEARCH_AGENT_SECRETS_KEY to enter keys here, or use an env var."}>
              <Input id="gp-key" type="password" autoComplete="off" disabled={!initial.encryption_enabled} value={googleKey} onChange={(e) => setGoogleKey(e.target.value)} />
            </Row>
            <Row id="gp-env" label="…or env var"><Input id="gp-env" placeholder="GOOGLE_PSE_API_KEY" value={w.search.google_pse_key_env ?? ""} onChange={(e) => setW({ ...w, search: { ...w.search, google_pse_key_env: e.target.value } })} /></Row>
          </div>
        )}
        {w.search.backend === "brave" && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Row id="br-key" label="API key" hint={initial.encryption_enabled ? "Paste to set or replace; stored encrypted." : "Set RESEARCH_AGENT_SECRETS_KEY to enter keys here, or use an env var."}>
              <Input id="br-key" type="password" autoComplete="off" disabled={!initial.encryption_enabled} value={braveKey} onChange={(e) => setBraveKey(e.target.value)} />
            </Row>
            <Row id="br-env" label="…or env var"><Input id="br-env" placeholder="BRAVE_API_KEY" value={w.search.api_key_env ?? ""} onChange={(e) => setW({ ...w, search: { ...w.search, api_key_env: e.target.value } })} /></Row>
          </div>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          <Row id="se-country" label="Country"><Input id="se-country" placeholder="US" value={w.search.country ?? ""} onChange={(e) => setW({ ...w, search: { ...w.search, country: e.target.value } })} /></Row>
          <Row id="se-lang" label="Language"><Input id="se-lang" placeholder="en" value={w.search.search_lang ?? ""} onChange={(e) => setW({ ...w, search: { ...w.search, search_lang: e.target.value } })} /></Row>
          <Row id="se-safe" label="Safe search">
            <NativeSelect id="se-safe" value={w.search.safesearch ?? ""} onChange={(e) => setW({ ...w, search: { ...w.search, safesearch: (e.target.value || undefined) as never } })}>
              <option value="">Default</option><option value="off">Off</option><option value="moderate">Moderate</option><option value="strict">Strict</option>
            </NativeSelect>
          </Row>
          {(w.search.backend === "google_pse" || w.search.backend === "brave") && (
            <Row id="se-budget" label="Monthly query budget" hint="Paid backends only; 0 = no limit.">
              <Input id="se-budget" type="number" min={0} value={w.search.monthly_budget ?? ""} onChange={(e) => setW({ ...w, search: { ...w.search, monthly_budget: num(e.target.value) } })} />
            </Row>
          )}
        </div>
      </Section>
      <Button type="submit" className="self-start" disabled={save.isPending}>{save.isPending ? "Saving…" : "Save settings"}</Button>
    </form>
  );
}
