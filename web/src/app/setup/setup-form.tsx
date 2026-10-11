"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";

import { ApiStatus } from "@/components/api-status";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, errorMessage, unwrap } from "@/lib/api/client";
import { APP_NAME } from "@/lib/app";
import { ME_KEY, applySession } from "@/lib/session";

export const SETUP_STATUS_KEY = ["setup", "status"] as const;

/** First-run setup: claims a fresh install with the token from the api log. */
export function SetupForm() {
  const router = useRouter();
  const qc = useQueryClient();
  const { data: status, isPending } = useQuery({ queryKey: SETUP_STATUS_KEY, queryFn: async () => unwrap(await api.GET("/setup/status")) });
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  useEffect(() => {
    if (status && !status.needs_setup) router.replace("/login");
  }, [status, router]);

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const password = String(f.get("password") ?? "");
    if (password !== String(f.get("confirm") ?? "")) {
      setError("The passwords don't match.");
      return;
    }
    setPending(true);
    setError(null);
    try {
      const session = unwrap(
        await api.POST("/setup", {
          body: {
            token: String(f.get("token") ?? "").trim(),
            workspace: String(f.get("workspace") ?? ""),
            name: String(f.get("name") ?? ""),
            email: String(f.get("email") ?? ""),
            password,
            example_brand: f.get("example") ? "benchwright" : undefined,
          },
        }),
      );
      applySession(session);
      qc.setQueryData(ME_KEY, session);
      qc.setQueryData(SETUP_STATUS_KEY, { ...status!, needs_setup: false });
      router.replace("/start");
    } catch (err) {
      setError(errorMessage(err));
      setPending(false);
    }
  }

  if (isPending || !status?.needs_setup) {
    return <p className="text-sm text-muted-foreground">Loading…</p>;
  }
  const hasExample = status.example_brands.includes("benchwright");
  return (
    <Card className="w-full max-w-md">
      <CardHeader>
        <CardTitle>Set up {APP_NAME}</CardTitle>
        <CardDescription>Create the admin account and your workspace. You&apos;ll connect a model and describe your brand next.</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="flex flex-col gap-4" onSubmit={onSubmit}>
          <div className="flex flex-col gap-2">
            <Label htmlFor="token">Setup token</Label>
            <Input id="token" name="token" required autoFocus autoComplete="off" className="font-mono" />
            <p className="text-xs text-muted-foreground">
              Printed in the api service&apos;s log when it starts (<code>setup_token=…</code>), or the value of <code>RESEARCH_AGENT_SETUP_TOKEN</code> if you set one.
              With Docker Compose: <code>docker compose logs api | grep setup_token</code>.
            </p>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="workspace">Brand or workspace name</Label>
            <Input id="workspace" name="workspace" required placeholder="Your brand" />
            <p className="text-xs text-muted-foreground">Prompts call your brand by this name. You can rename it later.</p>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="flex flex-col gap-2">
              <Label htmlFor="name">Your name</Label>
              <Input id="name" name="name" autoComplete="name" />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="email">Email</Label>
              <Input id="email" name="email" type="email" required autoComplete="email" />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="password">Password</Label>
              <Input id="password" name="password" type="password" required minLength={12} autoComplete="new-password" />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="confirm">Confirm password</Label>
              <Input id="confirm" name="confirm" type="password" required minLength={12} autoComplete="new-password" />
            </div>
          </div>
          <p className="-mt-2 text-xs text-muted-foreground">At least 12 characters.</p>
          {hasExample && (
            <label className="flex items-start gap-2 text-sm">
              <input type="checkbox" name="example" className="mt-1" />
              <span>
                Start with an example brand
                <span className="block text-xs text-muted-foreground">Fills the Brand page with Benchwright, a fictional open-source LLM evaluation tool whose niche is discussed daily on Hacker News, so your first monitor run finds opportunities quickly. Edit or replace it later.</span>
              </span>
            </label>
          )}
          {!status.encryption_enabled && (
            <Alert>
              <AlertDescription className="text-xs">
                <code>RESEARCH_AGENT_SECRETS_KEY</code> isn&apos;t set on the server, so API keys can&apos;t be entered in Settings; you&apos;d reference environment variables instead.
                To enable it, set it to the output of <code>openssl rand -base64 32</code> on the api and worker and restart them.
              </AlertDescription>
            </Alert>
          )}
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <Button type="submit" disabled={pending}>
            {pending ? "Setting up…" : "Create workspace"}
          </Button>
        </form>
      </CardContent>
      <CardFooter>
        <ApiStatus />
      </CardFooter>
    </Card>
  );
}
