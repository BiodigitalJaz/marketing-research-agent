"use client";

import { Suspense } from "react";

import { ResearchView } from "@/components/research/research-view";

export default function ResearchPage() {
  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-4">
      <header>
        <h1 className="text-xl font-semibold">Research</h1>
        <p className="text-sm text-muted-foreground">Ask questions or run deep research over collected conversations and the web. Every claim cites its source.</p>
      </header>
      <Suspense fallback={<p className="text-sm text-muted-foreground">Loading…</p>}>
        <ResearchView />
      </Suspense>
    </div>
  );
}
