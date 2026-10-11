"use client";

import { ItemsPanel } from "@/components/collection/items-panel";
import { MonitorsPanel } from "@/components/collection/monitors-panel";
import { SourcesPanel } from "@/components/collection/sources-panel";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useTabParam } from "@/lib/tab";
import { Suspense } from "react";

const TABS = ["monitors", "sources", "items"] as const;

export default function SourcesPage() {
  return (
    <Suspense fallback={null}>
      <Sources />
    </Suspense>
  );
}

function Sources() {
  const tab = useTabParam(TABS, "monitors");
  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6">
      <header>
        <h1 className="text-xl font-semibold">Monitors &amp; sources</h1>
        <p className="text-sm text-muted-foreground">Collection is free: Google Alerts feeds, RSS, Hacker News, and self-hosted web search.</p>
      </header>
      <Tabs key={tab} defaultValue={tab}>
        <TabsList className="flex-wrap">
          <TabsTrigger value="monitors">Monitors</TabsTrigger>
          <TabsTrigger value="sources">Sources</TabsTrigger>
          <TabsTrigger value="items">Collected items</TabsTrigger>
        </TabsList>
        <TabsContent value="monitors" className="pt-4"><MonitorsPanel /></TabsContent>
        <TabsContent value="sources" className="pt-4"><SourcesPanel /></TabsContent>
        <TabsContent value="items" className="pt-4"><ItemsPanel /></TabsContent>
      </Tabs>
    </div>
  );
}
