"use client";

import { KnowledgePanel } from "@/components/settings/knowledge-panel";
import { LanesPanel } from "@/components/settings/lanes-panel";
import { MCPPanel } from "@/components/settings/mcp-panel";
import { ModelsPanel } from "@/components/settings/models-panel";
import { PlaygroundPanel } from "@/components/settings/playground-panel";
import { PromptsPanel } from "@/components/settings/prompts-panel";
import { ProvidersPanel } from "@/components/settings/providers-panel";
import { RoutesPanel } from "@/components/settings/routes-panel";
import { RunsPanel } from "@/components/settings/runs-panel";
import { UsersPanel } from "@/components/settings/users-panel";
import { WorkspacePanel } from "@/components/settings/workspace-panel";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useSession } from "@/lib/session";
import { useTabParam } from "@/lib/tab";
import { Suspense } from "react";

const TABS = [
  { value: "providers", label: "Providers & keys", el: <ProvidersPanel /> },
  { value: "models", label: "Models", el: <ModelsPanel /> },
  { value: "routes", label: "Routes", el: <RoutesPanel /> },
  { value: "prompts", label: "Prompts", el: <PromptsPanel /> },
  { value: "lanes", label: "Lanes", el: <LanesPanel /> },
  { value: "mcp", label: "MCP servers", el: <MCPPanel /> },
  { value: "memory", label: "Memory bank", el: <KnowledgePanel /> },
  { value: "workspace", label: "Mode, embeddings & search", el: <WorkspacePanel /> },
  { value: "playground", label: "Playground", el: <PlaygroundPanel /> },
  { value: "runs", label: "Activity", el: <RunsPanel /> },
  { value: "users", label: "Workspace & users", el: <UsersPanel /> },
];

export default function SettingsPage() {
  return (
    <Suspense fallback={null}>
      <Settings />
    </Suspense>
  );
}

function Settings() {
  const { isAdmin } = useSession();
  const tab = useTabParam(TABS.map((t) => t.value), "providers");
  if (!isAdmin) return <p className="text-sm text-muted-foreground">Settings are available to admins.</p>;
  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-6">
      <header>
        <h1 className="text-xl font-semibold">Settings</h1>
        <p className="text-sm text-muted-foreground">Everything about how the agent thinks: providers, models, routes, prompts, and tools.</p>
      </header>
      <Tabs key={tab} defaultValue={tab}>
        <TabsList className="h-auto flex-wrap">
          {TABS.map((t) => <TabsTrigger key={t.value} value={t.value}>{t.label}</TabsTrigger>)}
        </TabsList>
        {TABS.map((t) => <TabsContent key={t.value} value={t.value} className="pt-4">{t.el}</TabsContent>)}
      </Tabs>
    </div>
  );
}
