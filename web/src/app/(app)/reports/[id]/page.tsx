"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { use } from "react";

import { ReportMarkdown, SourcesList } from "@/components/research/report-markdown";
import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button";
import { api, errorMessage, unwrap } from "@/lib/api/client";
import { useSession } from "@/lib/session";

export default function ReportPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  const { workspace } = useSession();
  const { data, error, isPending } = useQuery({ queryKey: ["report", id], queryFn: async () => unwrap(await api.GET("/reports/{id}", { params: { path: { id } } })) });
  if (isPending) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (error || !data) return <p className="text-sm text-destructive">{errorMessage(error)}</p>;
  return (
    <article className="mx-auto flex max-w-3xl flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Link href="/reports" className="text-sm text-muted-foreground hover:underline">← Reports</Link>
        <div className="flex gap-2">
          {data.job_id && <Link href={`/research?job=${data.job_id}`} className={buttonVariants({ size: "sm", variant: "ghost" })}>How it was researched</Link>}
          <a href={`/api/v1/reports/${id}/export?format=md&workspace=${workspace.id}`} download className={buttonVariants({ size: "sm", variant: "outline" })}>Export Markdown</a>
        </div>
      </div>
      <p className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <Badge variant="outline">AI-generated research — review before sharing</Badge>
        <span>{new Date(data.created_at).toLocaleString()}</span>
        <span>· {data.citations.length} citations</span>
      </p>
      <ReportMarkdown markdown={data.markdown} charts={data.charts} citations={data.citations} />
      <SourcesList citations={data.citations} />
    </article>
  );
}
