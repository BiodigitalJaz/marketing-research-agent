"use client";

import { useSearchParams } from "next/navigation";

/** The ?tab= search parameter when it names one of tabs, else fallback. Callers must sit inside <Suspense>. */
export function useTabParam(tabs: readonly string[], fallback: string): string {
  const t = useSearchParams().get("tab");
  return t && tabs.includes(t) ? t : fallback;
}
