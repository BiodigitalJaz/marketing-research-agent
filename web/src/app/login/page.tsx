import type { Metadata } from "next";

import { APP_NAME } from "@/lib/app";

import { LoginForm } from "./login-form";

export const metadata: Metadata = { title: `Sign in · ${APP_NAME}` };

export default function LoginPage() {
  return (
    <main className="flex flex-1 items-center justify-center p-4">
      <LoginForm />
    </main>
  );
}
