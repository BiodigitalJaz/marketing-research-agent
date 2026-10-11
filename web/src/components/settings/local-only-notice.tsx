"use client";

import Link from "next/link";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { useLocalOnly } from "@/lib/local-only";

/** Shown in Settings panels that pick providers while local-only mode is on. */
export function LocalOnlyNotice() {
  const { localOnly } = useLocalOnly();
  if (!localOnly) return null;
  return (
    <Alert>
      <AlertDescription className="text-sm">
        <strong>Local-only mode is on.</strong> Hosted providers are skipped for every model call, so their models are greyed out here.
        Change it under <Link href="/settings?tab=workspace" className="underline underline-offset-2">Mode, embeddings &amp; search</Link>.
      </AlertDescription>
    </Alert>
  );
}
