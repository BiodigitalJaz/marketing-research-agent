"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { useCrudMutation } from "@/components/brand/use-crud";
import { FACTOR_LABEL, LANE_LABEL } from "@/components/inbox/meta";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, unwrap, type Schemas } from "@/lib/api/client";

import { num, Row, Section } from "./common";

type LaneConfig = Schemas["LaneConfig"];
const KEY = ["settings", "lanes"] as const;
const FACTORS = ["relevance", "intent", "fit", "recency", "reach", "deadline"];

export function LanesPanel() {
  const { data } = useQuery({ queryKey: KEY, queryFn: async () => unwrap(await api.GET("/settings/lanes")).items });
  return (
    <Section title="Lanes" description="How each lane is scored and how many picks it gets per day. Weights are normalized, so only their ratios matter.">
      <div className="grid gap-4 lg:grid-cols-3">{data?.map((l) => <LaneCard key={l.lane} lane={l} />)}</div>
    </Section>
  );
}

function LaneCard({ lane }: { lane: LaneConfig }) {
  const [l, setL] = useState<LaneConfig>(lane);
  const save = useCrudMutation(KEY, async (v: LaneConfig) => unwrap(await api.PUT("/settings/lanes/{lane}", { params: { path: { lane: v.lane } }, body: v })), `${LANE_LABEL[lane.lane]} saved`);
  const total = FACTORS.reduce((n, f) => n + (l.weights[f] ?? 0), 0) || 1;
  return (
    <form className="flex flex-col gap-3 rounded-lg border p-4" onSubmit={(e) => { e.preventDefault(); save.mutate(l); }}>
      <h3 className="font-medium">{LANE_LABEL[l.lane]}</h3>
      <div className="flex flex-col gap-2">
        {FACTORS.map((f) => (
          <label key={f} className="grid grid-cols-[6rem_1fr_3rem] items-center gap-2 text-sm">
            <span>{FACTOR_LABEL[f]}</span>
            <input type="range" min={0} max={1} step={0.05} value={l.weights[f] ?? 0} aria-label={`${FACTOR_LABEL[f]} weight`}
              onChange={(e) => setL({ ...l, weights: { ...l.weights, [f]: Number(e.target.value) } })} />
            <span className="text-right text-xs text-muted-foreground tabular-nums">{Math.round(((l.weights[f] ?? 0) / total) * 100)}%</span>
          </label>
        ))}
      </div>
      <div className="grid grid-cols-3 gap-2">
        <Row id={`${l.lane}-q`} label="Picks/day"><Input id={`${l.lane}-q`} type="number" min={0} max={100} value={l.daily_quota} onChange={(e) => setL({ ...l, daily_quota: num(e.target.value) ?? 0 })} /></Row>
        <Row id={`${l.lane}-f`} label="Fresh (h)"><Input id={`${l.lane}-f`} type="number" min={1} value={l.freshness_hours} onChange={(e) => setL({ ...l, freshness_hours: num(e.target.value) ?? 1 })} /></Row>
        <Row id={`${l.lane}-m`} label="Min score"><Input id={`${l.lane}-m`} type="number" min={0} max={1} step={0.05} value={l.min_score} onChange={(e) => setL({ ...l, min_score: num(e.target.value) ?? 0 })} /></Row>
      </div>
      <Button type="submit" size="sm" className="self-start" disabled={save.isPending}>Save</Button>
    </form>
  );
}
