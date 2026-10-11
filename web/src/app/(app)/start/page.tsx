"use client";

import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, Circle } from "lucide-react";
import Link from "next/link";

import { buttonVariants } from "@/components/ui/button";
import { api, unwrap } from "@/lib/api/client";
import { useSession } from "@/lib/session";

const CHECKLIST_KEY = ["setup", "checklist"] as const;

/** Getting started: the steps from a fresh install to a working inbox. */
export default function StartPage() {
  const { isAdmin, workspace } = useSession();
  const { data } = useQuery({ queryKey: CHECKLIST_KEY, queryFn: async () => unwrap(await api.GET("/setup/checklist")) });

  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6">
      <header>
        <h1 className="text-xl font-semibold">Getting started</h1>
        <p className="text-sm text-muted-foreground">
          {data && data.done === data.total
            ? `${workspace.name} is set up. Opportunities will land in the inbox as monitors collect.`
            : "A few steps turn this workspace into a working inbox. Each one opens the page where it's done; come back here to see what's left."}
        </p>
      </header>
      {data && (
        <p className="text-sm font-medium">
          {data.done} of {data.total} done
        </p>
      )}
      <ol className="flex flex-col divide-y rounded-lg border">
        {data?.items.map((it, i) => (
          <li key={it.id} className="flex items-start gap-3 p-4">
            {it.done ? <CheckCircle2 className="mt-0.5 size-5 shrink-0 text-green-600" aria-label="Done" /> : <Circle className="mt-0.5 size-5 shrink-0 text-muted-foreground" aria-label="To do" />}
            <div className="flex flex-1 flex-col gap-1">
              <p className="font-medium">
                {i + 1}. {it.title}
              </p>
              <p className="text-sm text-muted-foreground">{it.detail}</p>
            </div>
            {it.admin && !isAdmin ? (
              <span className="shrink-0 text-xs text-muted-foreground">Ask an admin</span>
            ) : (
              <Link href={it.href} className={buttonVariants({ size: "sm", variant: it.done ? "ghost" : "outline" })}>
                {it.done ? "Review" : "Open"}
              </Link>
            )}
          </li>
        ))}
      </ol>
      <p className="text-xs text-muted-foreground">
        Hosted and local models both work: any OpenAI-compatible server (Ollama, LM Studio, llama.cpp, vLLM) or the Anthropic and OpenAI APIs. See the deployment guide for model sizing.
      </p>
    </div>
  );
}
