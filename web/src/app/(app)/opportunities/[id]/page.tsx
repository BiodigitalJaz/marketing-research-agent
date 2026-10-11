"use client";

import { use } from "react";

import { OpportunityDetail } from "@/components/opportunity/opportunity-detail";

export default function OpportunityPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return (
    <div className="mx-auto max-w-6xl">
      <OpportunityDetail id={id} />
    </div>
  );
}
