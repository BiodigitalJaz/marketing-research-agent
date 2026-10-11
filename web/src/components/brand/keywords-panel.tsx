"use client";

import { useQuery } from "@tanstack/react-query";
import { XIcon } from "lucide-react";
import { useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { api, unwrap, type Schemas } from "@/lib/api/client";
import { useSession } from "@/lib/session";

import { useCrudMutation } from "./use-crud";

type Keyword = Schemas["Keyword"];
type Lane = Schemas["Lane"];
const KEY = ["keywords"] as const;
const LANE_LABEL: Record<Lane, string> = { inbound: "Inbound", media: "Media & speaking", thought_leadership: "Thought leadership" };

export function KeywordsPanel() {
  const { canEdit } = useSession();
  const { data, isPending } = useQuery({ queryKey: KEY, queryFn: async () => unwrap(await api.GET("/keywords")).items });
  const create = useCrudMutation(KEY, async (body: Schemas["KeywordInput"]) => unwrap(await api.POST("/keywords", { body })), "Keyword added");
  const remove = useCrudMutation(KEY, async (id: string) => unwrap(await api.DELETE("/keywords/{id}", { params: { path: { id } } })), "Keyword removed");

  const [term, setTerm] = useState("");
  const [kind, setKind] = useState<"track" | "negative">("track");
  const [lane, setLane] = useState<Lane | "">("");

  const groups: { kind: Keyword["kind"]; title: string; hint: string }[] = [
    { kind: "track", title: "Tracked", hint: "Terms monitors search for." },
    { kind: "negative", title: "Negative", hint: "Items matching these are dropped before scoring." },
  ];

  return (
    <section className="flex flex-col gap-6">
      {canEdit && (
        <form
          className="flex flex-col gap-2 sm:flex-row sm:items-end"
          onSubmit={(e) => {
            e.preventDefault();
            if (!term.trim()) return;
            create.mutate({ term, kind, lane: lane || null }, { onSuccess: () => setTerm("") });
          }}
        >
          <Input aria-label="Keyword" placeholder="Add a keyword or phrase" value={term} onChange={(e) => setTerm(e.target.value)} className="sm:flex-1" />
          <NativeSelect aria-label="Kind" value={kind} onChange={(e) => setKind(e.target.value as "track" | "negative")} className="sm:w-36">
            <option value="track">Track</option>
            <option value="negative">Negative</option>
          </NativeSelect>
          <NativeSelect aria-label="Lane" value={lane} onChange={(e) => setLane(e.target.value as Lane | "")} className="sm:w-48">
            <option value="">All lanes</option>
            {(Object.keys(LANE_LABEL) as Lane[]).map((l) => (
              <option key={l} value={l}>
                {LANE_LABEL[l]}
              </option>
            ))}
          </NativeSelect>
          <Button type="submit" disabled={create.isPending}>
            Add
          </Button>
        </form>
      )}
      {isPending ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : (
        groups.map((g) => {
          const items = data?.filter((k) => k.kind === g.kind) ?? [];
          return (
            <div key={g.kind} className="flex flex-col gap-2">
              <div>
                <h2 className="text-sm font-medium">
                  {g.title} <span className="text-muted-foreground">({items.length})</span>
                </h2>
                <p className="text-xs text-muted-foreground">{g.hint}</p>
              </div>
              {items.length === 0 ? (
                <p className="text-sm text-muted-foreground">None.</p>
              ) : (
                <ul className="flex flex-wrap gap-1.5">
                  {items.map((k) => (
                    <li key={k.id}>
                      <Badge variant={g.kind === "negative" ? "outline" : "secondary"} className="h-auto gap-1 py-0.5">
                        {k.term}
                        {k.lane && <span className="text-muted-foreground">· {LANE_LABEL[k.lane]}</span>}
                        {canEdit && (
                          <button type="button" aria-label={`Remove ${k.term}`} className="opacity-60 hover:opacity-100" onClick={() => remove.mutate(k.id)}>
                            <XIcon className="size-3" />
                          </button>
                        )}
                      </Badge>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          );
        })
      )}
    </section>
  );
}
