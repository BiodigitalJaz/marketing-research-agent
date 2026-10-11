"use client";

import { useQuery } from "@tanstack/react-query";

import { api, unwrap } from "@/lib/api/client";

/**
 * Local-only mode for Settings UIs (admins only: the workspace settings
 * endpoint requires admin). `blocked` is true for a provider the router would
 * skip: local-only is on and the provider isn't marked local.
 */
export function useLocalOnly(enabled = true) {
  const { data: ws } = useQuery({ queryKey: ["settings", "workspace"], queryFn: async () => unwrap(await api.GET("/settings/workspace")), enabled });
  const { data: providers } = useQuery({ queryKey: ["settings", "providers"], queryFn: async () => unwrap(await api.GET("/settings/providers")), enabled });
  const localOnly = ws?.local_only ?? false;
  const isLocal = (providerID: string) => providers?.items.find((p) => p.id === providerID)?.is_local ?? false;
  return { localOnly, blocked: (providerID: string) => localOnly && !isLocal(providerID) };
}

/** Suffix for a hosted model's option label while local-only mode is on. */
export const OFF_IN_LOCAL_ONLY = " · hosted, off in local-only mode";
