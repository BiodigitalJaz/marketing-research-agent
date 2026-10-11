"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useState } from "react";

import { ReportMarkdown } from "@/components/research/report-markdown";
import { Badge } from "@/components/ui/badge";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api, unwrap } from "@/lib/api/client";
import { useSession } from "@/lib/session";

const LABEL: Record<string, string> = {
  projectbrief: "Project brief", productContext: "Product context", systemPatterns: "System patterns",
  techContext: "Tech context", activeContext: "Active context", progress: "Progress",
};

/** Read-only view of the memory bank: core files generated from the project's internal docs. */
export default function MemoryPage() {
  const { isAdmin } = useSession();
  const { data, isPending } = useQuery({ queryKey: ["memory-bank"], queryFn: async () => unwrap(await api.GET("/knowledge/memory-bank")).items });
  const [tab, setTab] = useState<string | null>(null);
  const current = data?.find((f) => f.name === (tab ?? data[0]?.name));
  return (
    <div className="mx-auto flex max-w-4xl flex-col gap-4">
      <header>
        <h1 className="text-xl font-semibold">Memory bank</h1>
        <p className="text-sm text-muted-foreground">
          What the research agent knows about the project, summarized from its internal docs. Internal only: not for public posts.
          {isAdmin && <> Configure it under <Link href="/settings" className="underline underline-offset-2">Settings → Memory bank</Link>.</>}
        </p>
      </header>
      {isPending ? <p className="text-sm text-muted-foreground">Loading…</p> : !data?.length ? (
        <div className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">No memory bank yet. Once a docs repository is connected and synced, the files appear here.</div>
      ) : (
        <>
          <Tabs value={current?.name ?? ""} onValueChange={setTab}>
            <TabsList className="h-auto flex-wrap">{data.map((f) => <TabsTrigger key={f.name} value={f.name}>{LABEL[f.name] ?? f.name}</TabsTrigger>)}</TabsList>
          </Tabs>
          {current && (
            <article className="flex flex-col gap-2 rounded-lg border p-4">
              <p className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <Badge variant="outline">Generated from the docs — summaries, not verified behavior</Badge>
                <span>{current.name}.md</span>
                {current.commit_sha && <span>· docs at {current.commit_sha.slice(0, 7)}</span>}
                <span>· updated {new Date(current.updated_at).toLocaleString()}</span>
              </p>
              <ReportMarkdown markdown={current.content} citations={[]} />
            </article>
          )}
        </>
      )}
    </div>
  );
}
