"use client";

import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api, errorMessage, unwrap } from "@/lib/api/client";
import { useSession } from "@/lib/session";

import { DismissDialog } from "./dismiss-dialog";
import { EmptyInbox } from "./empty-inbox";
import { LANE_LABEL, type Lane, type Opportunity } from "./meta";
import { OpportunityCard } from "./opportunity-card";

type LaneTab = Lane | "all";
type Status = "new" | "saved" | "snoozed" | "acted" | "dismissed" | "expired";

export function Inbox() {
  const { canEdit } = useSession();
  const qc = useQueryClient();
  const [lane, setLane] = useState<LaneTab>("all");
  const [status, setStatus] = useState<Status>("new");
  const [selected, setSelected] = useState(0);
  const [dismissing, setDismissing] = useState<Opportunity | null>(null);
  const router = useRouter();
  const detail = useCallback((o: Opportunity) => router.push(`/opportunities/${o.id}`), [router]);

  const key = ["opportunities", lane, status];
  const query = useInfiniteQuery({
    queryKey: key,
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) =>
      unwrap(await api.GET("/opportunities", { params: { query: { lane: lane === "all" ? undefined : lane, status, cursor: pageParam, limit: 30 } } })),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    refetchInterval: 60_000,
  });
  const items = useMemo(() => query.data?.pages.flatMap((p) => p.items) ?? [], [query.data]);
  const counts = query.data?.pages[0]?.counts;
  const featured = items.filter((o) => o.featured);
  const rest = items.filter((o) => !o.featured);
  const ordered = useMemo(() => [...featured, ...rest], [featured, rest]);

  const refresh = useCallback(() => void qc.invalidateQueries({ queryKey: ["opportunities"] }), [qc]);

  const setStatusFor = useCallback(async (o: Opportunity, body: { status: "saved" | "acted" | "dismissed" | "snoozed" | "new"; reason?: never | string; note?: string; snoozed_until?: string }) => {
    try {
      unwrap(await api.POST("/opportunities/{id}/status", { params: { path: { id: o.id } }, body: body as never }));
      toast.success(body.status === "dismissed" ? "Dismissed" : body.status === "snoozed" ? "Snoozed" : `Marked ${body.status}`);
      refresh();
    } catch (e) {
      toast.error(errorMessage(e));
    }
  }, [refresh]);

  const open = useCallback((o: Opportunity) => {
    window.open(o.url, "_blank", "noopener,noreferrer");
    void api.POST("/opportunities/{id}/events", { params: { path: { id: o.id } }, body: { event: "opened_link" } });
  }, []);

  // Keyboard: j/k move, s save, d dismiss, o open, Enter details.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const t = e.target as HTMLElement;
      if (dismissing || t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || e.metaKey || e.ctrlKey || e.altKey) return;
      const cur = ordered[selected];
      switch (e.key) {
        case "j": setSelected((i) => Math.min(i + 1, ordered.length - 1)); break;
        case "k": setSelected((i) => Math.max(i - 1, 0)); break;
        case "o": if (cur) open(cur); break;
        case "Enter": if (cur) detail(cur); break;
        case "s": if (cur && canEdit) void setStatusFor(cur, { status: "saved" }); break;
        case "d": if (cur && canEdit) setDismissing(cur); break;
        default: return;
      }
      e.preventDefault();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [ordered, selected, dismissing, detail, canEdit, open, setStatusFor]);

  useEffect(() => {
    document.querySelector(`[data-selected="true"]`)?.scrollIntoView({ block: "nearest" });
  }, [selected]);

  const card = (o: Opportunity) => {
    const i = ordered.indexOf(o);
    return (
      <OpportunityCard key={o.id} opp={o} selected={i === selected} canEdit={canEdit}
        onSelect={() => setSelected(i)} onOpen={() => open(o)} onDetail={() => detail(o)}
        onSave={() => void setStatusFor(o, { status: "saved" })}
        onDismiss={() => setDismissing(o)}
        onSnooze={(days) => void setStatusFor(o, { status: "snoozed", snoozed_until: new Date(Date.now() + days * 86_400_000).toISOString() })}
        onActed={() => void setStatusFor(o, { status: "acted" })} />
    );
  };

  const total = counts ? Object.values(counts).reduce((n, c) => n + c.new, 0) : 0;
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Tabs value={lane} onValueChange={(v) => { setLane(v as LaneTab); setSelected(0); }}>
          <TabsList className="flex-wrap">
            <TabsTrigger value="all">All {status === "new" && counts ? `(${total})` : ""}</TabsTrigger>
            {(Object.keys(LANE_LABEL) as Lane[]).map((l) => (
              <TabsTrigger key={l} value={l}>{LANE_LABEL[l]} {status === "new" && counts?.[l] ? `(${counts[l].new})` : ""}</TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
        <NativeSelect aria-label="Status" className="w-40" value={status} onChange={(e) => { setStatus(e.target.value as Status); setSelected(0); }}>
          {(["new", "saved", "snoozed", "acted", "dismissed", "expired"] as Status[]).map((s) => <option key={s} value={s}>{s[0].toUpperCase() + s.slice(1)}</option>)}
        </NativeSelect>
      </div>
      <p className="text-xs text-muted-foreground">Keys: j/k move · o open · Enter details{canEdit && " · s save · d dismiss"}</p>

      {query.isPending ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : items.length === 0 ? (
        status === "new" ? <EmptyInbox /> : (
          <div className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">No {status} opportunities.</div>
        )
      ) : (
        <>
          {featured.length > 0 && (
            <section className="flex flex-col gap-2">
              <h2 className="text-sm font-semibold">Today&apos;s picks</h2>
              <ul className="flex flex-col gap-2">{featured.map(card)}</ul>
            </section>
          )}
          {rest.length > 0 && (
            <section className="flex flex-col gap-2">
              {featured.length > 0 && <h2 className="text-sm font-semibold">More</h2>}
              <ul className="flex flex-col gap-2">{rest.map(card)}</ul>
            </section>
          )}
          {query.hasNextPage && (
            <Button variant="outline" className="self-center" disabled={query.isFetchingNextPage} onClick={() => query.fetchNextPage()}>
              {query.isFetchingNextPage ? "Loading…" : "Show more"}
            </Button>
          )}
        </>
      )}

      <DismissDialog key={dismissing?.id ?? "none"} open={dismissing !== null} onCancel={() => setDismissing(null)}
        onConfirm={(reason, note) => { if (dismissing) void setStatusFor(dismissing, { status: "dismissed", reason, note: note || undefined }); setDismissing(null); }} />
    </div>
  );
}
