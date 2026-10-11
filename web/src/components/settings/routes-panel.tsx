"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { useCrudMutation } from "@/components/brand/use-crud";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { api, unwrap, type Schemas } from "@/lib/api/client";
import { OFF_IN_LOCAL_ONLY, useLocalOnly } from "@/lib/local-only";
import { cn } from "@/lib/utils";

import { Check, EFFORTS, num, Row, Section, Status } from "./common";
import { ExtraFields } from "./extra-fields";
import { LocalOnlyNotice } from "./local-only-notice";

type Route = Schemas["Route"];
type Model = Schemas["Model"];
const KEY = ["settings", "routes"] as const;

const TASK_HELP: Record<string, string> = {
  classify: "Sorts collected items into lanes. Needs structured output.",
  score: "Judges relevance, intent, and fit. Needs structured output.",
  draft: "Writes replies, pitches, and comments.",
  research: "Runs the research agent. Needs tool calls.",
  summarize: "Condenses long tool results during research.",
};

export function RoutesPanel() {
  const { data } = useQuery({ queryKey: KEY, queryFn: async () => unwrap(await api.GET("/settings/routes")) });
  const routes = data?.items;
  const { data: models } = useQuery({ queryKey: ["settings", "models"], queryFn: async () => unwrap(await api.GET("/settings/models")).items });
  const { data: mcp } = useQuery({ queryKey: ["settings", "mcp"], queryFn: async () => unwrap(await api.GET("/settings/mcp-servers")).items });
  return (
    <Section title="Task routes" description="Which model handles each task, with an optional fallback (any provider). In local-only mode only local providers are used.">
      <LocalOnlyNotice />
      {/* Research has many more fields than the other tasks, so it gets its own
          full-width row at the end; the short cards pair up without gaps. */}
      <div className="grid items-start gap-4 lg:grid-cols-2">
        {routes && [...routes].sort((a, b) => Number(a.task === "research") - Number(b.task === "research")).map((r) => (
          <div key={r.task} className={r.task === "research" ? "lg:col-span-2" : undefined}>
            <RouteCard route={r} models={models ?? []} mcp={mcp ?? []} builtins={data?.builtin_tools ?? []} />
          </div>
        ))}
      </div>
    </Section>
  );
}

const TOOL_HELP: Record<string, string> = {
  search_corpus: "Search collected content", get_item: "Read a collected item", search_brand: "Brand profile and portfolio",
  opportunity_stats: "Opportunity and item counts", make_chart: "Charts from tool data", web_search: "Web search (needs web on)",
  fetch_url: "Read web pages (robots.txt respected)", view_image: "Look at images (vision models)",
  search_knowledge: "Search internal docs", read_doc: "Read an internal doc section", read_memory_bank: "Read memory bank files",
};

/** Stable comparison key for a route's editable fields. */
function routeKey(x: Route): string {
  const sort = (v: unknown): unknown =>
    v && typeof v === "object" && !Array.isArray(v)
      ? Object.fromEntries(Object.entries(v as Record<string, unknown>).filter(([, val]) => val !== undefined && val !== null && val !== "").sort(([a], [b]) => a.localeCompare(b)).map(([k, val]) => [k, sort(val)]))
      : v;
  return JSON.stringify(sort({ p: x.provider_id, m: x.model, fp: x.fallback_provider_id ?? null, fm: x.fallback_model ?? null, params: x.params ?? {}, tools: x.tools ?? [], mcp: x.mcp_server_ids ?? [] }));
}

function RouteCard({ route, models, mcp, builtins }: { route: Route; models: Model[]; mcp: Schemas["MCPServer"][]; builtins: string[] }) {
  const [r, setR] = useState<Route>(route);
  const { blocked } = useLocalOnly();
  const save = useCrudMutation(
    KEY,
    async (v: Route) =>
      unwrap(await api.PUT("/settings/routes/{task}", { params: { path: { task: v.task } }, body: {
        provider_id: v.provider_id, model: v.model, fallback_provider_id: v.fallback_provider_id ?? null, fallback_model: v.fallback_model ?? null,
        params: v.params, tools: v.tools, mcp_server_ids: v.mcp_server_ids,
      } })),
    `Route for ${route.task} saved`,
  );
  const enabled = models.filter((m) => m.enabled);
  const key = (m: Model) => `${m.provider_id}::${m.model}`;
  const primary = enabled.find((m) => m.provider_id === r.provider_id && m.model === r.model);
  const caps = primary?.capabilities;
  const p: Schemas["Params"] = r.params ?? {};
  const setP = (patch: Partial<Schemas["Params"]>) => setR({ ...r, params: { ...p, ...patch } });
  const mcpIDs = r.mcp_server_ids ?? [];
  // Edits stay local until Save route; flag them so they aren't mistaken for saved values.
  const dirty = routeKey(r) !== routeKey(route);

  return (
    <form className={cn("flex flex-col gap-3 rounded-lg border p-4", dirty && "border-amber-500/60")} onSubmit={(e) => { e.preventDefault(); save.mutate(r); }}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h3 className="font-medium capitalize">{r.task}</h3>
          <p className="text-xs text-muted-foreground">{TASK_HELP[r.task]}</p>
        </div>
        <Status ok={route.usable}>{route.usable ? `Using ${route.effective.join(" → ")}` : "Not usable"}</Status>
      </div>
      {route.problems.length > 0 && (
        <ul className="list-disc pl-5 text-xs text-muted-foreground">{route.problems.map((x) => <li key={x}>{x}</li>)}</ul>
      )}
      <Row id={`${r.task}-primary`} label="Model">
        <NativeSelect id={`${r.task}-primary`} value={primary ? key(primary) : ""} onChange={(e) => { const [pid, model] = e.target.value.split("::"); setR({ ...r, provider_id: pid, model }); }}>
          <option value="" disabled>Choose a declared model</option>
          {enabled.map((m) => <option key={key(m)} value={key(m)} disabled={blocked(m.provider_id)}>{m.provider_id} / {m.display_name || m.model}{blocked(m.provider_id) ? OFF_IN_LOCAL_ONLY : ""}</option>)}
        </NativeSelect>
      </Row>
      <Row id={`${r.task}-fb`} label="Fallback" hint="Used when the primary fails or isn't allowed (local-only mode).">
        <NativeSelect id={`${r.task}-fb`} value={r.fallback_provider_id ? `${r.fallback_provider_id}::${r.fallback_model}` : ""}
          onChange={(e) => { if (!e.target.value) { setR({ ...r, fallback_provider_id: null, fallback_model: null }); return; } const [pid, model] = e.target.value.split("::"); setR({ ...r, fallback_provider_id: pid, fallback_model: model }); }}>
          <option value="">None</option>
          {enabled.map((m) => <option key={key(m)} value={key(m)} disabled={blocked(m.provider_id)}>{m.provider_id} / {m.display_name || m.model}{blocked(m.provider_id) ? OFF_IN_LOCAL_ONLY : ""}</option>)}
        </NativeSelect>
      </Row>
      <div className="grid grid-cols-2 gap-3">
        <Row id={`${r.task}-max`} label="Max tokens">
          <Input id={`${r.task}-max`} type="number" min={0} value={p.max_tokens ?? ""} onChange={(e) => setP({ max_tokens: num(e.target.value) })} />
        </Row>
        {caps?.sampling && (
          <Row id={`${r.task}-temp`} label="Temperature">
            <Input id={`${r.task}-temp`} type="number" step="0.05" min={0} max={2} value={p.temperature ?? ""} onChange={(e) => setP({ temperature: num(e.target.value) })} />
          </Row>
        )}
        {caps?.thinking === "effort" && (
          <Row id={`${r.task}-effort`} label="Thinking effort">
            <NativeSelect id={`${r.task}-effort`} value={p.thinking?.effort ?? ""} onChange={(e) => setP({ thinking: e.target.value ? { effort: e.target.value as never, summarized: p.thinking?.summarized } : undefined })}>
              <option value="">Model default</option>
              {EFFORTS.map((x) => <option key={x} value={x}>{x}</option>)}
            </NativeSelect>
          </Row>
        )}
        {caps?.thinking === "budget" && (
          <Row id={`${r.task}-budget`} label="Thinking budget">
            <Input id={`${r.task}-budget`} type="number" min={1024} value={p.thinking?.budget_tokens ?? ""} onChange={(e) => { const b = num(e.target.value); setP({ thinking: b ? { budget_tokens: b } : undefined }); }} />
          </Row>
        )}
        {r.task === "research" && (
          <>
            <Row id={`${r.task}-steps`} label="Max agent steps">
              <Input id={`${r.task}-steps`} type="number" min={0} max={100} value={p.max_steps ?? ""} onChange={(e) => setP({ max_steps: num(e.target.value) })} />
            </Row>
            <Row id={`${r.task}-timeout`} label="Time limit (s)" hint="Default 300 chat / 900 deep; at most 1800.">
              <Input id={`${r.task}-timeout`} type="number" min={0} max={1800} value={p.timeout_s ?? ""} onChange={(e) => setP({ timeout_s: num(e.target.value) })} />
            </Row>
            <Row id={`${r.task}-budget-tokens`} label="Token budget per job" hint="Input + output tokens; on reaching it the agent answers from what it has.">
              <Input id={`${r.task}-budget-tokens`} type="number" min={0} step={10000} value={p.token_budget ?? ""} onChange={(e) => setP({ token_budget: num(e.target.value) })} />
            </Row>
          </>
        )}
      </div>
      <ExtraFields id={`${r.task}-extra`} value={p.extra} onChange={(v) => setP({ extra: v })} />
      {r.task === "research" && builtins.length > 0 && (
        <fieldset className="grid gap-1.5 sm:grid-cols-2">
          <legend className="mb-1.5 text-sm font-medium">Built-in tools</legend>
          {builtins.map((t) => {
            const enabled = (r.tools ?? []).length === 0 || (r.tools ?? []).includes(t);
            return (
              <Check key={t} id={`${r.task}-tool-${t}`} label={`${t} — ${TOOL_HELP[t] ?? ""}`} checked={enabled}
                onChange={(v) => {
                  const cur = (r.tools ?? []).length === 0 ? builtins : r.tools ?? [];
                  const next = v ? [...new Set([...cur, t])] : cur.filter((x) => x !== t);
                  setR({ ...r, tools: next.length === builtins.length ? [] : next });
                }} />
            );
          })}
        </fieldset>
      )}
      {caps?.tools && mcp.length > 0 && (
        <fieldset className="flex flex-col gap-1.5">
          <legend className="text-sm font-medium">MCP servers</legend>
          {mcp.map((s) => (
            <Check key={s.id} id={`${r.task}-mcp-${s.id}`} label={`${s.name} (${s.allowed_tools.length} tools)`} disabled={!s.enabled}
              checked={mcpIDs.includes(s.id)}
              onChange={(v) => setR({ ...r, mcp_server_ids: v ? [...mcpIDs, s.id] : mcpIDs.filter((x) => x !== s.id) })} />
          ))}
        </fieldset>
      )}
      <div className="flex items-center gap-3">
        <Button type="submit" size="sm" className="self-start" variant={dirty ? "default" : "outline"} disabled={save.isPending || !primary}>{save.isPending ? "Saving…" : "Save route"}</Button>
        {dirty && <span className="text-xs font-medium text-amber-600 dark:text-amber-400">Unsaved changes</span>}
      </div>
    </form>
  );
}
