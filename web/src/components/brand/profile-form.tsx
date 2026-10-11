"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { ListInput } from "@/components/list-input";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { api, errorMessage, unwrap, type Schemas } from "@/lib/api/client";
import { useSession } from "@/lib/session";

import { Field } from "./field";

type Profile = Schemas["BrandProfile"];
const KEY = ["brand-profile"];

export function ProfileForm() {
  const { data, isPending, error } = useQuery({
    queryKey: KEY,
    queryFn: async () => unwrap(await api.GET("/brand/profile")),
  });
  if (isPending) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (error) return <p className="text-sm text-destructive">{errorMessage(error)}</p>;
  // Keyed so the editor re-initializes from the server copy after each save.
  return <ProfileEditor key={data.updated_at ?? "new"} initial={data.profile} updatedAt={data.updated_at ?? null} />;
}

function ProfileEditor({ initial, updatedAt }: { initial: Profile; updatedAt: string | null }) {
  const { canEdit } = useSession();
  const qc = useQueryClient();
  const [p, setP] = useState<Profile>(initial);

  const save = useMutation({
    mutationFn: async (body: Profile) => unwrap(await api.PUT("/brand/profile", { body })),
    onSuccess: (res) => {
      qc.setQueryData(KEY, res);
      toast.success("Brand profile saved");
    },
    onError: (err) => toast.error(errorMessage(err)),
  });

  const set = <K extends keyof Profile>(k: K, v: Profile[K]) => setP({ ...p, [k]: v });
  const setTone = <K extends keyof Profile["tone"]>(k: K, v: Profile["tone"][K]) => setP({ ...p, tone: { ...p.tone, [k]: v } });
  const setT = <K extends keyof Profile["targeting"]>(k: K, v: Profile["targeting"][K]) => setP({ ...p, targeting: { ...p.targeting, [k]: v } });

  return (
    <form
      className="flex flex-col gap-6"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate(p);
      }}
    >
      <fieldset disabled={!canEdit} className="grid gap-6 md:grid-cols-2">
        <Field id="services" label="Services" hint="What you offer.">
          <ListInput id="services" value={p.services} onChange={(v) => set("services", v)} disabled={!canEdit} />
        </Field>
        <Field id="value_props" label="Value propositions">
          <ListInput id="value_props" value={p.value_props} onChange={(v) => set("value_props", v)} disabled={!canEdit} />
        </Field>
        <Field id="differentiators" label="Differentiators">
          <ListInput id="differentiators" value={p.differentiators} onChange={(v) => set("differentiators", v)} disabled={!canEdit} />
        </Field>
        <Field id="voice" label="Voice" hint="How drafts should sound.">
          <Textarea id="voice" rows={4} value={p.tone.voice} onChange={(e) => setTone("voice", e.target.value)} />
        </Field>
        <Field id="tone_do" label="Do">
          <ListInput id="tone_do" value={p.tone.do} onChange={(v) => setTone("do", v)} disabled={!canEdit} />
        </Field>
        <Field id="tone_dont" label="Don't (banned claims and phrases)" hint="Short entries (up to three words) or entries in &quot;quotes&quot; are banned phrases: a draft containing one is regenerated once, then blocked. Longer entries are rules the model checks each draft against.">
          <ListInput id="tone_dont" value={p.tone.dont} onChange={(v) => setTone("dont", v)} disabled={!canEdit} />
        </Field>
        <div className="md:col-span-2">
          <Field id="examples" label="Voice examples" hint="Short lines that capture the voice.">
            <ListInput id="examples" value={p.tone.examples} onChange={(v) => setTone("examples", v)} disabled={!canEdit} />
          </Field>
        </div>
      </fieldset>
      <fieldset disabled={!canEdit} className="grid gap-6 border-t pt-6 md:grid-cols-2">
        <legend className="sr-only">Targeting</legend>
        <div className="md:col-span-2">
          <h2 className="text-sm font-semibold">Targeting</h2>
          <p className="text-xs text-muted-foreground">Steers how collected items are classified and scored.</p>
        </div>
        <Field id="t-prio" label="Prioritize" hint="Audiences and situations to score up.">
          <ListInput id="t-prio" value={p.targeting.prioritize} onChange={(v) => setT("prioritize", v)} disabled={!canEdit} />
        </Field>
        <Field id="t-avoid" label="Avoid" hint="Audiences and situations to score down.">
          <ListInput id="t-avoid" value={p.targeting.avoid} onChange={(v) => setT("avoid", v)} disabled={!canEdit} />
        </Field>
        <Field id="t-topics" label="Themes to own" hint="Thought-leadership topics.">
          <ListInput id="t-topics" value={p.targeting.topics} onChange={(v) => setT("topics", v)} disabled={!canEdit} />
        </Field>
        <Field id="t-media" label="Valuable media opportunities">
          <ListInput id="t-media" value={p.targeting.media_targets} onChange={(v) => setT("media_targets", v)} disabled={!canEdit} />
        </Field>
        <Field id="t-geo" label="Regions served">
          <ListInput id="t-geo" value={p.targeting.geography} onChange={(v) => setT("geography", v)} disabled={!canEdit} />
        </Field>
        <Field id="t-venues" label="Venues we don't post in" hint="Listening only: these never produce inbound opportunities.">
          <ListInput id="t-venues" value={p.targeting.excluded_venues} onChange={(v) => setT("excluded_venues", v)} disabled={!canEdit} />
        </Field>
      </fieldset>
      {canEdit && (
        <div className="flex items-center gap-3">
          <Button type="submit" disabled={save.isPending}>
            {save.isPending ? "Saving…" : "Save profile"}
          </Button>
          {updatedAt && <span className="text-xs text-muted-foreground">Last saved {new Date(updatedAt).toLocaleString()}</span>}
        </div>
      )}
    </form>
  );
}
