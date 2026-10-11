"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";

import { api, unwrap } from "@/lib/api/client";

/** Explains an empty New list and what to do next. */
export function EmptyInbox() {
  const { data: checklist } = useQuery({ queryKey: ["setup", "checklist"], queryFn: async () => unwrap(await api.GET("/setup/checklist")) });
  const left = checklist ? checklist.total - checklist.done : 0;
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
      <p className="font-medium text-foreground">Nothing new yet.</p>
      {left > 0 ? (
        <p>
          Setup isn&apos;t finished: {left} step{left === 1 ? "" : "s"} left. <Link href="/start" className="underline underline-offset-2">Getting started</Link> shows what&apos;s missing.
        </p>
      ) : (
        <ul className="flex max-w-lg flex-col gap-1 text-left">
          <li>• Monitors collect on their schedule; <Link href="/sources" className="underline underline-offset-2">Run now</Link> collects immediately and shows progress.</li>
          <li>• Each item is classified and scored by the model, which takes a while on a local model. Opportunities appear here as they pass the lane&apos;s minimum score.</li>
          <li>• <Link href="/sources?tab=items" className="underline underline-offset-2">Collected items</Link> shows why items were filtered. Few items? Add Google Alerts or RSS feeds and tracked keywords.</li>
        </ul>
      )}
    </div>
  );
}
