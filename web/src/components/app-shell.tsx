"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { usePathname } from "next/navigation";

import { Button } from "@/components/ui/button";
import { api, unwrap } from "@/lib/api/client";
import { APP_NAME, SOURCE_URL } from "@/lib/app";
import { useLocalOnly } from "@/lib/local-only";
import { cn } from "@/lib/utils";
import { useLogout, useSession } from "@/lib/session";

// Sections land milestone by milestone; unavailable ones are shown disabled.
const NAV = [
  { href: "/inbox", label: "Inbox", ready: true, admin: false },
  { href: "/research", label: "Research", ready: true, admin: false },
  { href: "/reports", label: "Reports", ready: true, admin: false },
  { href: "/memory", label: "Memory bank", ready: true, admin: false },
  { href: "/brand", label: "Brand", ready: true, admin: false },
  { href: "/sources", label: "Monitors & sources", ready: true, admin: false },
  { href: "/settings", label: "Settings", ready: true, admin: true },
];

export function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const { session, workspace, isAdmin } = useSession();
  const logout = useLogout();
  const { data: checklist } = useQuery({ queryKey: ["setup", "checklist"], queryFn: async () => unwrap(await api.GET("/setup/checklist")), staleTime: 60_000 });
  const setupLeft = checklist ? checklist.total - checklist.done : 0;
  const { localOnly } = useLocalOnly(isAdmin);

  return (
    <div className="flex min-h-screen flex-col md:flex-row">
      <aside className="flex shrink-0 flex-col gap-1 border-b bg-muted/30 p-3 md:w-56 md:border-r md:border-b-0">
        <div className="px-2 pb-3">
          <p className="text-sm font-semibold">{APP_NAME}</p>
          <p className="text-xs text-muted-foreground">{workspace.name}</p>
          {localOnly && (
            <Link href="/settings?tab=workspace" title="Every model call uses local providers only" className="mt-1 inline-block rounded-md border px-1.5 py-0.5 text-[11px] text-muted-foreground hover:bg-muted">
              Local-only mode
            </Link>
          )}
        </div>
        <nav className="flex flex-row flex-wrap gap-1 md:flex-col">
          {setupLeft > 0 && (
            <Link
              href="/start"
              className={cn("rounded-md px-2 py-1.5 text-sm hover:bg-muted", pathname.startsWith("/start") && "bg-muted font-medium")}
            >
              Getting started <span className="text-xs text-muted-foreground">· {setupLeft} left</span>
            </Link>
          )}
          {NAV.filter((item) => !item.admin || isAdmin).map((item) =>
            item.ready ? (
              <Link
                key={item.href}
                href={item.href}
                className={cn(
                  "rounded-md px-2 py-1.5 text-sm hover:bg-muted",
                  pathname.startsWith(item.href) && "bg-muted font-medium",
                )}
              >
                {item.label}
              </Link>
            ) : (
              <span key={item.href} className="cursor-not-allowed rounded-md px-2 py-1.5 text-sm text-muted-foreground/60" title="Coming soon">
                {item.label}
              </span>
            ),
          )}
        </nav>
        <div className="flex items-center justify-between gap-2 px-2 pt-2 md:mt-auto md:flex-col md:items-stretch md:pt-4">
          <Link href="/account" className="truncate text-xs text-muted-foreground hover:underline" title="Account and password">
            {session.user.email} · {workspace.role}
          </Link>
          <Button variant="outline" size="sm" onClick={logout}>
            Sign out
          </Button>
          <a href={SOURCE_URL} target="_blank" rel="noreferrer noopener" className="text-xs text-muted-foreground hover:underline">
            Source code · AGPL-3.0
          </a>
        </div>
      </aside>
      <main className="min-w-0 flex-1 p-4 md:p-8">{children}</main>
    </div>
  );
}
