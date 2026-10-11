"use client";

import { BrandPackActions } from "@/components/brand/brand-pack";
import { CompetitorsPanel } from "@/components/brand/competitors-panel";
import { ICPsPanel } from "@/components/brand/icps-panel";
import { KeywordsPanel } from "@/components/brand/keywords-panel";
import { PortfolioPanel } from "@/components/brand/portfolio-panel";
import { ProfileForm } from "@/components/brand/profile-form";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useTabParam } from "@/lib/tab";
import { Suspense } from "react";

const TABS = ["profile", "portfolio", "icps", "keywords", "competitors"] as const;

export default function BrandPage() {
  return (
    <Suspense fallback={null}>
      <Brand />
    </Suspense>
  );
}

function Brand() {
  const tab = useTabParam(TABS, "profile");
  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Brand</h1>
          <p className="text-sm text-muted-foreground">
            What the agent knows about your brand. Scoring and drafts use this, so keep it accurate.
          </p>
        </div>
        <BrandPackActions />
      </header>
      <Tabs key={tab} defaultValue={tab}>
        <TabsList className="flex-wrap">
          <TabsTrigger value="profile">Profile</TabsTrigger>
          <TabsTrigger value="portfolio">Portfolio</TabsTrigger>
          <TabsTrigger value="icps">ICPs</TabsTrigger>
          <TabsTrigger value="keywords">Keywords</TabsTrigger>
          <TabsTrigger value="competitors">Competitors</TabsTrigger>
        </TabsList>
        <TabsContent value="profile" className="pt-4"><ProfileForm /></TabsContent>
        <TabsContent value="portfolio" className="pt-4"><PortfolioPanel /></TabsContent>
        <TabsContent value="icps" className="pt-4"><ICPsPanel /></TabsContent>
        <TabsContent value="keywords" className="pt-4"><KeywordsPanel /></TabsContent>
        <TabsContent value="competitors" className="pt-4"><CompetitorsPanel /></TabsContent>
      </Tabs>
    </div>
  );
}
