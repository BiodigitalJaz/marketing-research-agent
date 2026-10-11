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
import { APP_NAME, SOURCE_URL } from "@/lib/app";
import { ME_KEY, applySession } from "@/lib/session";

/** After sign-in: Getting started while setup steps remain, else the inbox. */
async function landingPage(): Promise<string> {
  try {
    const c = unwrap(await api.GET("/setup/checklist"));
    return c.done < c.total ? "/start" : "/inbox";
  } catch {
    return "/inbox";
  }
}

export function LoginForm() {
  const router = useRouter();
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  // A fresh install has no users: send the visitor to first-run setup.
  const { data: setup } = useQuery({ queryKey: ["setup", "status"], queryFn: async () => unwrap(await api.GET("/setup/status")) });
  useEffect(() => {
    if (setup?.needs_setup) router.replace("/setup");
  }, [setup, router]);

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    setPending(true);
    setError(null);
    try {
      const session = unwrap(
        await api.POST("/auth/login", {
          body: { email: String(form.get("email") ?? ""), password: String(form.get("password") ?? "") },
        }),
      );
      applySession(session);
      qc.setQueryData(ME_KEY, session);
      router.replace(await landingPage());
    } catch (err) {
      setError(errorMessage(err));
      setPending(false);
    }
  }

  return (
    <Card className="w-full max-w-sm">
      <CardHeader>
        <CardTitle>{APP_NAME}</CardTitle>
        <CardDescription>Sign in to your workspace.</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="flex flex-col gap-4" onSubmit={onSubmit}>
          <div className="flex flex-col gap-2">
            <Label htmlFor="email">Email</Label>
            <Input id="email" name="email" type="email" autoComplete="email" required autoFocus />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="password">Password</Label>
            <Input id="password" name="password" type="password" autoComplete="current-password" required />
          </div>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <Button type="submit" disabled={pending}>
            {pending ? "Signing in…" : "Sign in"}
          </Button>
        </form>
      </CardContent>
      <CardFooter className="flex flex-col items-start gap-2">
        <ApiStatus />
        <a href={SOURCE_URL} target="_blank" rel="noreferrer noopener" className="text-xs text-muted-foreground hover:underline">
          Open source (AGPL-3.0)
        </a>
      </CardFooter>
    </Card>
  );
}
