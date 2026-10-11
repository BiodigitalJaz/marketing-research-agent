"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";

import { Check } from "@/components/settings/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, unwrap, type Schemas } from "@/lib/api/client";
import { useSession } from "@/lib/session";
import { cn } from "@/lib/utils";

import { ReportMarkdown } from "./report-markdown";
import { Timeline } from "./timeline";

type Step = Schemas["ResearchStep"];

const TEMPLATE_LABEL: Record<string, string> = {
  freeform: "Freeform", competitor_teardown: "Competitor teardown", market_scan: "Market scan", pain_points: "Pain points", topic_deep_dive: "Topic deep dive",
};
const ACTIVE = new Set(["queued", "running"]);

export function ResearchView() {
  const params = useSearchParams();
  const jobId = params.get("job");
  const [archived, setArchived] = useState(false);
  const { data } = useQuery({
    queryKey: ["research", "list", archived],
    queryFn: async () => unwrap(await api.GET("/research", { params: { query: { archived } } })),
    refetchInterval: 10_000,
  });
  return (
    <div className="grid gap-6 lg:grid-cols-[16rem_minmax(0,1fr)]">
      <aside className="flex flex-col gap-2">
        <Link href="/research" className={cn("rounded-md px-2 py-1.5 text-sm font-medium hover:bg-muted", !jobId && "bg-muted")}>+ New research</Link>
        <label className="flex items-center gap-2 px-2 text-xs text-muted-foreground">
          <input type="checkbox" checked={archived} onChange={(e) => setArchived(e.target.checked)} />
          Show archived
        </label>
        {data?.items.length === 0 && <p className="px-2 text-xs text-muted-foreground">{archived ? "No archived research." : "No research yet."}</p>}
        <ul className="flex flex-col gap-0.5">
          {data?.items.map((j) => (
            <li key={j.id}>
              <Link href={`/research?job=${j.id}`} className={cn("flex flex-col rounded-md px-2 py-1.5 hover:bg-muted", jobId === j.id && "bg-muted")}>
                <span className="line-clamp-2 text-sm">{j.question}</span>
                <span className="text-xs text-muted-foreground">{j.mode === "deep" ? "Deep research" : "Chat"} · {j.status}</span>
              </Link>
            </li>
          ))}
        </ul>
      </aside>
      <main className="min-w-0">{jobId ? <JobView key={jobId} id={jobId} /> : <NewResearch templates={data?.templates ?? Object.keys(TEMPLATE_LABEL)} />}</main>
    </div>
  );
}

function NewResearch({ templates }: { templates: string[] }) {
  const { canEdit } = useSession();
  const router = useRouter();
  const qc = useQueryClient();
  const [mode, setMode] = useState<"chat" | "deep">("chat");
  const [question, setQuestion] = useState("");
  const [template, setTemplate] = useState("freeform");
  const [useWeb, setUseWeb] = useState(true);
  const create = useMutation({
    mutationFn: async () => unwrap(await api.POST("/research", { body: { mode, question, template: mode === "deep" ? template as never : null, use_web: useWeb } })),
    onSuccess: (j) => { void qc.invalidateQueries({ queryKey: ["research"] }); router.push(`/research?job=${j.id}`); },
    onError: (e) => toast.error(errorMessage(e)),
  });
  if (!canEdit) return <p className="text-sm text-muted-foreground">Viewers can read past research and reports. Ask an editor to start new research.</p>;
  return (
    <form className="flex max-w-2xl flex-col gap-4" onSubmit={(e) => { e.preventDefault(); if (question.trim()) create.mutate(); }}>
      <Tabs value={mode} onValueChange={(v) => setMode(v as "chat" | "deep")}>
        <TabsList>
          <TabsTrigger value="chat">Ask a question</TabsTrigger>
          <TabsTrigger value="deep">Deep research</TabsTrigger>
        </TabsList>
      </Tabs>
      <p className="text-sm text-muted-foreground">
        {mode === "chat" ? "A quick answer from collected content and the web, with citations. You can ask follow-ups."
          : "Plans sub-questions, gathers evidence, checks key claims, and writes a cited report with charts. Takes a few minutes."}
      </p>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="r-q">Question</Label>
        <Textarea id="r-q" rows={3} maxLength={4000} required value={question} onChange={(e) => setQuestion(e.target.value)}
          placeholder={mode === "chat" ? "What frustrates small agencies about their project management tools?" : "Which pain points come up most for our audience this month, and how do competitors respond?"} />
      </div>
      {mode === "deep" && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="r-t">Report template</Label>
          <NativeSelect id="r-t" className="w-60" value={template} onChange={(e) => setTemplate(e.target.value)}>
            {templates.map((t) => <option key={t} value={t}>{TEMPLATE_LABEL[t] ?? t}</option>)}
          </NativeSelect>
        </div>
      )}
      <Check id="r-web" label="Use the web" hint="Off: only collected content and brand data. Pages are fetched politely (robots.txt, rate limits)." checked={useWeb} onChange={setUseWeb} />
      <Button type="submit" className="self-start" disabled={create.isPending || !question.trim()}>{create.isPending ? "Starting…" : mode === "chat" ? "Ask" : "Start research"}</Button>
    </form>
  );
}

/** Streams steps over SSE while the job runs, starting after the steps already loaded. */
function useJobStream(id: string, active: boolean, startAfter: number, onStep: (s: Step) => void, onDone: () => void) {
  const { workspace } = useSession();
  const lastSeq = useRef(0);
  const handlers = useRef({ onStep, onDone });
  useEffect(() => {
    handlers.current = { onStep, onDone };
  });
  useEffect(() => {
    if (!active) return;
    lastSeq.current = Math.max(lastSeq.current, startAfter);
    const es = new EventSource(`/api/v1/research/${id}/stream?workspace=${workspace.id}&after=${lastSeq.current}`);
    es.addEventListener("step", (ev) => {
      const s = JSON.parse((ev as MessageEvent).data) as Step;
      if (s.seq <= lastSeq.current) return;
      lastSeq.current = s.seq;
      handlers.current.onStep(s);
      if (s.kind === "done") {
        es.close();
        handlers.current.onDone();
      }
    });
    return () => es.close();
  }, [id, active, startAfter, workspace.id]);
}

function JobView({ id }: { id: string }) {
  const { canEdit, isAdmin, session } = useSession();
  const router = useRouter();
  const qc = useQueryClient();
  const key = ["research", id];
  const { data, error } = useQuery({ queryKey: key, queryFn: async () => unwrap(await api.GET("/research/{id}", { params: { path: { id } } })) });
  const [live, setLive] = useState<Step[]>([]);
  const [followUp, setFollowUp] = useState("");
  const job = data?.job;
  const active = job ? ACTIVE.has(job.status) : false;
  const refresh = () => { void qc.invalidateQueries({ queryKey: key }); void qc.invalidateQueries({ queryKey: ["research"] }); };
  useJobStream(id, active, data?.steps.at(-1)?.seq ?? 0, (s) => setLive((prev) => [...prev, s]), () => { setLive([]); refresh(); });

  const cancel = useMutation({
    mutationFn: async () => unwrap(await api.POST("/research/{id}/cancel", { params: { path: { id } } })),
    onSuccess: refresh, onError: (e) => toast.error(errorMessage(e)),
  });
  const archive = useMutation({
    mutationFn: async (archived: boolean) => unwrap(await api.POST("/research/{id}/archive", { params: { path: { id } }, body: { archived } })),
    onSuccess: (_, archived) => { toast.success(archived ? "Archived" : "Restored"); refresh(); }, onError: (e) => toast.error(errorMessage(e)),
  });
  const remove = useMutation({
    mutationFn: async () => unwrap(await api.DELETE("/research/{id}", { params: { path: { id } } })),
    onSuccess: () => { toast.success("Research deleted"); void qc.invalidateQueries({ queryKey: ["research"] }); router.push("/research"); },
    onError: (e) => toast.error(errorMessage(e)),
  });
  const send = useMutation({
    mutationFn: async () => unwrap(await api.POST("/research/{id}/messages", { params: { path: { id } }, body: { content: followUp } })),
    onSuccess: () => { setFollowUp(""); setLive([]); refresh(); }, onError: (e) => toast.error(errorMessage(e)),
  });

  if (error) return <p className="text-sm text-destructive">{errorMessage(error)}</p>;
  if (!data || !job) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const seen = new Set(data.steps.map((s) => s.seq));
  const steps = [...data.steps, ...live.filter((s) => !seen.has(s.seq))];
  const progress = job.progress as { phase?: string; steps_done?: number; steps_planned?: number };

  return (
    <div className="flex flex-col gap-4">
      <header className="flex flex-col gap-1">
        <div className="flex items-start justify-between gap-3">
          <h1 className="text-lg font-semibold break-words">{job.question}</h1>
          <div className="flex shrink-0 gap-2">
            {canEdit && active && <Button size="sm" variant="outline" onClick={() => cancel.mutate()} disabled={cancel.isPending}>Cancel</Button>}
            {canEdit && !active && (
              <Button size="sm" variant="outline" onClick={() => archive.mutate(!job.archived)} disabled={archive.isPending}>{job.archived ? "Unarchive" : "Archive"}</Button>
            )}
            {canEdit && !active && (isAdmin || job.created_by === session.user.id) && (
              <Button size="sm" variant="ghost" disabled={remove.isPending}
                onClick={() => { if (confirm(job.report_id ? "Delete this research? Its report is kept under Reports." : "Delete this research and its chat?")) remove.mutate(); }}>
                Delete
              </Button>
            )}
          </div>
        </div>
        <p className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <Badge variant={job.status === "failed" ? "destructive" : job.status === "succeeded" ? "secondary" : "outline"}>{job.status}</Badge>
          {job.archived && <Badge variant="outline">archived</Badge>}
          <span>{job.mode === "deep" ? `Deep research · ${TEMPLATE_LABEL[job.template ?? "freeform"] ?? job.template}` : "Chat"}</span>
          <span>· {job.use_web ? "web on" : "collected content only"}</span>
          {active && progress.phase && <span>· {progress.phase}{progress.steps_planned ? ` ${progress.steps_done ?? 0}/${progress.steps_planned}` : ""}</span>}
          {job.report_id && <Link href={`/reports/${job.report_id}`} className="font-medium text-foreground underline underline-offset-2">Open report</Link>}
        </p>
        {job.error && job.status === "failed" && <p className="text-sm text-destructive">{job.error}</p>}
      </header>

      {job.mode === "chat" ? (
        <ChatThread messages={data.messages} steps={steps} active={active} />
      ) : (
        <Timeline steps={steps} />
      )}

      {job.mode === "chat" && canEdit && !active && (
        <form className="flex flex-col gap-2" onSubmit={(e) => { e.preventDefault(); if (followUp.trim()) send.mutate(); }}>
          <Label htmlFor="r-follow">Follow-up</Label>
          <Textarea id="r-follow" rows={2} maxLength={4000} value={followUp} onChange={(e) => setFollowUp(e.target.value)} />
          <Button type="submit" size="sm" className="self-start" disabled={send.isPending || !followUp.trim()}>Ask</Button>
        </form>
      )}
    </div>
  );
}

function ChatThread({ messages, steps, active }: { messages: Schemas["ChatMessage"][]; steps: Step[]; active: boolean }) {
  // Steps since the last answer show what the assistant is doing now.
  const lastAnswer = steps.findLastIndex((s) => s.kind === "answer" || s.kind === "done");
  const current = active ? steps.slice(lastAnswer + 1) : [];
  return (
    <div className="flex flex-col gap-4">
      {messages.map((m) => m.role === "user" ? (
        <div key={m.id} className="self-end rounded-lg bg-muted px-3 py-2 text-sm whitespace-pre-wrap">{m.content}</div>
      ) : (
        <article key={m.id} className="flex flex-col gap-2 rounded-lg border p-4">
          <Badge variant="outline" className="self-start">AI answer — check the sources</Badge>
          <ReportMarkdown markdown={m.content} citations={m.sources.map((s) => ({ marker: s.marker, url: s.source.url, title: s.source.title }))} />
          {m.sources.length > 0 && (
            <ol className="flex flex-col gap-0.5 border-t pt-2 text-xs text-muted-foreground">
              {m.sources.map((s) => <li key={s.marker}>{s.marker} <a href={s.source.url} target="_blank" rel="noreferrer noopener" className="hover:underline">{s.source.title || s.source.url}</a></li>)}
            </ol>
          )}
        </article>
      ))}
      {active && (
        <div className="rounded-lg border border-dashed p-3">
          <p className="mb-2 flex items-center gap-2 text-sm text-muted-foreground"><span className="size-2 animate-pulse rounded-full bg-primary" aria-hidden />Researching…</p>
          <Timeline steps={current} compact />
        </div>
      )}
    </div>
  );
}
