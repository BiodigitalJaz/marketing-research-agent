"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { createContext, useContext, useEffect } from "react";

import { ApiError, api, setSessionHeaders, unwrap, type Schemas } from "@/lib/api/client";

type Session = Schemas["Session"];
type Workspace = Session["workspaces"][number];

type SessionValue = {
  session: Session;
  workspace: Workspace;
  canEdit: boolean;
  isAdmin: boolean;
};

const SessionContext = createContext<SessionValue | null>(null);

export const ME_KEY = ["me"] as const;

export async function fetchMe(): Promise<Session> {
  return unwrap(await api.GET("/auth/me"));
}

/** Applies a session's CSRF token and first workspace to API requests. */
export function applySession(s: Session) {
  setSessionHeaders(s.csrf_token, s.workspaces[0]?.id ?? "");
}

/** Gates its children on a valid session; redirects to /login otherwise. */
export function SessionGate({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const { data, error, isPending } = useQuery({
    queryKey: ME_KEY,
    queryFn: async () => {
      const s = await fetchMe();
      applySession(s);
      return s;
    },
    retry: (count, err) => !(err instanceof ApiError && err.status === 401) && count < 2,
  });

  const unauthenticated = error instanceof ApiError && error.status === 401;
  useEffect(() => {
    if (unauthenticated) router.replace("/login");
  }, [unauthenticated, router]);

  if (isPending || unauthenticated) {
    return <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">Loading…</div>;
  }
  if (error || !data) {
    return <div className="flex flex-1 items-center justify-center text-sm text-destructive">Could not load your session.</div>;
  }
  const workspace = data.workspaces[0];
  if (!workspace) {
    return <div className="flex flex-1 items-center justify-center text-sm">Your account is not a member of any workspace.</div>;
  }
  const value: SessionValue = {
    session: data,
    workspace,
    canEdit: workspace.role === "admin" || workspace.role === "editor",
    isAdmin: workspace.role === "admin",
  };
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionValue {
  const v = useContext(SessionContext);
  if (!v) throw new Error("useSession must be used inside SessionGate");
  return v;
}

export function useLogout() {
  const qc = useQueryClient();
  const router = useRouter();
  return async () => {
    await api.POST("/auth/logout");
    setSessionHeaders("", "");
    qc.clear();
    router.replace("/login");
  };
}
