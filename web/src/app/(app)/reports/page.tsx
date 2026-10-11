"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";

import { api, unwrap } from "@/lib/api/client";

export default function ReportsPage() {
  const { data, isPending } = useQuery({ queryKey: ["reports"], queryFn: async () => unwrap(await api.GET("/reports")).items });
  return (
    <div className="mx-auto flex max-w-4xl flex-col gap-4">
      <header>
        <h1 className="text-xl font-semibold">Reports</h1>
        <p className="text-sm text-muted-foreground">Cited reports from deep research. Start one from <Link href="/research" className="underline underline-offset-2">Research</Link>.</p>
      </header>
      {isPending ? <p className="text-sm text-muted-foreground">Loading…</p> : !data?.length ? (
        <div className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">No reports yet.</div>
      ) : (
        <ul className="flex flex-col divide-y rounded-lg border">
          {data.map((r) => (
            <li key={r.id}>
              <Link href={`/reports/${r.id}`} className="flex flex-col gap-0.5 p-3 hover:bg-muted/50">
                <span className="font-medium">{r.title}</span>
                <span className="text-xs text-muted-foreground">{new Date(r.created_at).toLocaleString()} · {r.citation_count} citations{r.template ? ` · ${r.template.replaceAll("_", " ")}` : ""}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
