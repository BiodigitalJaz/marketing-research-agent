import type { Metadata } from "next";

import { APP_NAME } from "@/lib/app";

import { SetupForm } from "./setup-form";

export const metadata: Metadata = { title: `Set up · ${APP_NAME}` };

export default function SetupPage() {
  return (
    <main className="flex flex-1 items-center justify-center p-4">
      <SetupForm />
    </main>
  );
}
