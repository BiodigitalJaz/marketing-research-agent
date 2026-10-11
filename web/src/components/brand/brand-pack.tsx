"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { toast } from "sonner";

import { Button, buttonVariants } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, unwrap } from "@/lib/api/client";
import { ME_KEY, useSession } from "@/lib/session";

type Mode = "merge" | "replace";
type Result = { name: string; dry_run: boolean; renamed?: boolean; summary: { mode: string; profile_fields: number; keywords: number; competitors: number; icps: number; portfolio: number } };

const BRAND_KEYS = [["brand-profile"], ["keywords"], ["competitors"], ["icps"], ["portfolio"], ["setup", "checklist"]];

/** Import a brand pack (YAML or JSON) and export the brand as one. */
export function BrandPackActions() {
  const { canEdit, workspace } = useSession();
  const [open, setOpen] = useState(false);
  return (
    <div className="flex gap-2">
      <a href={`/api/v1/brand/export?workspace=${workspace.id}`} className={buttonVariants({ size: "sm", variant: "outline" })} download>
        Export
      </a>
      {canEdit && <Button size="sm" variant="outline" onClick={() => setOpen(true)}>Import</Button>}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader><DialogTitle>Import a brand pack</DialogTitle></DialogHeader>
          {open && <ImportForm onDone={() => setOpen(false)} />}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function ImportForm({ onDone }: { onDone: () => void }) {
  const qc = useQueryClient();
  const { isAdmin, workspace } = useSession();
  const [rename, setRename] = useState(true);
  const [text, setText] = useState("");
  const [mode, setMode] = useState<Mode>("merge");
  const [preview, setPreview] = useState<Result | null>(null);
  const [fileName, setFileName] = useState<string | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  // A single line ending in .yaml/.yml/.json is almost certainly a path typed into the box.
  const looksLikePath = /^\S+\.(ya?ml|json)$/i.test(text.trim()) && !text.trim().includes("\n");

  const run = useMutation({
    mutationFn: async (dry: boolean) => unwrap(await api.POST("/brand/import", { body: { pack: text, mode, dry_run: dry, rename_workspace: !dry && offerRename && rename } })) as Result,
    onSuccess: (r) => {
      if (r.dry_run) {
        setPreview(r);
        return;
      }
      for (const k of BRAND_KEYS) void qc.invalidateQueries({ queryKey: k });
      if (r.renamed) void qc.invalidateQueries({ queryKey: ME_KEY });
      toast.success(r.renamed ? `Imported ${r.name}; the workspace is now called ${r.name}` : `Imported ${r.name || "brand pack"}`);
      onDone();
    },
    onError: (e) => { setPreview(null); toast.error(errorMessage(e)); },
  });

  async function onFile(f: File | undefined) {
    if (!f) return;
    setText(await f.text());
    setFileName(f.name);
    setPreview(null);
  }

  const s = preview?.summary;
  // Prompts call the brand by the workspace name, so offer to match the pack's.
  const offerRename = isAdmin && !!preview?.name && preview.name.trim() !== workspace.name;
  return (
    <div className="flex flex-col gap-4 text-sm">
      <p className="text-muted-foreground">
        A brand pack describes your brand in one YAML or JSON file: profile, tone, targeting, keywords, competitors, ICPs, and portfolio. Use
        a file exported from another install, or write one from the example in the docs.
      </p>
      <div className="flex items-center gap-3">
        <input ref={fileRef} type="file" className="hidden" accept=".yaml,.yml,.json,text/yaml,application/json" onChange={(e) => void onFile(e.target.files?.[0])} />
        <Button type="button" size="sm" variant="outline" onClick={() => fileRef.current?.click()}>Choose file…</Button>
        <span className="text-xs text-muted-foreground">{fileName ? `Loaded ${fileName}` : "or paste the pack's contents below"}</span>
      </div>
      <Textarea rows={10} className="font-mono text-xs" placeholder={"name: Your Brand\nprofile:\n  services:\n    - What you offer"} value={text}
        onChange={(e) => { setText(e.target.value); setFileName(null); setPreview(null); }} />
      {looksLikePath && (
        <p className="text-xs text-amber-600 dark:text-amber-400">That looks like a file path. The browser can&apos;t read files by path: use Choose file…, or paste the file&apos;s contents.</p>
      )}
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1 font-medium">How to apply it</legend>
        <label className="flex items-start gap-2">
          <input type="radio" className="mt-1" checked={mode === "merge"} onChange={() => { setMode("merge"); setPreview(null); }} />
          <span>Add to the current brand<span className="block text-xs text-muted-foreground">Fills empty profile fields and adds keywords, competitors, ICPs, and portfolio items that aren&apos;t there yet. Nothing you have is changed.</span></span>
        </label>
        <label className="flex items-start gap-2">
          <input type="radio" className="mt-1" checked={mode === "replace"} onChange={() => { setMode("replace"); setPreview(null); }} />
          <span>Replace the brand<span className="block text-xs text-muted-foreground">The profile, keywords, competitors, ICPs, and portfolio are replaced by the pack&apos;s. Opportunities and collected items are kept.</span></span>
        </label>
      </fieldset>
      {s && (
        <div className="rounded-md bg-muted/50 p-3 text-xs">
          <p className="mb-1 font-medium text-foreground">{preview?.name ? `“${preview.name}”: ` : ""}{mode === "replace" ? "this will replace the brand with" : "this will add"}</p>
          <p>{s.profile_fields} profile field{s.profile_fields === 1 ? "" : "s"} · {s.keywords} keyword{s.keywords === 1 ? "" : "s"} · {s.competitors} competitor{s.competitors === 1 ? "" : "s"} · {s.icps} ICP{s.icps === 1 ? "" : "s"} · {s.portfolio} portfolio item{s.portfolio === 1 ? "" : "s"}</p>
        </div>
      )}
      {s && offerRename && (
        <label className="flex items-start gap-2">
          <input type="checkbox" className="mt-1" checked={rename} onChange={(e) => setRename(e.target.checked)} />
          <span>Also rename the workspace to “{preview?.name}”<span className="block text-xs text-muted-foreground">It&apos;s now “{workspace.name}”. Prompts call your brand by the workspace name, so research and drafts use it.</span></span>
        </label>
      )}
      <DialogFooter>
        <Button variant="ghost" onClick={onDone}>Cancel</Button>
        {s ? (
          <Button variant={mode === "replace" ? "destructive" : "default"} disabled={run.isPending} onClick={() => run.mutate(false)}>
            {run.isPending ? "Importing…" : mode === "replace" ? "Replace brand" : "Import"}
          </Button>
        ) : (
          <Button disabled={!text.trim() || looksLikePath || run.isPending} onClick={() => run.mutate(true)}>{run.isPending ? "Checking…" : "Preview"}</Button>
        )}
      </DialogFooter>
    </div>
  );
}
