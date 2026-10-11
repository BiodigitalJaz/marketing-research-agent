"use client";

import { Inbox } from "@/components/inbox/inbox";

export default function InboxPage() {
  return (
    <div className="mx-auto flex max-w-4xl flex-col gap-4">
      <header>
        <h1 className="text-xl font-semibold">Inbox</h1>
        <p className="text-sm text-muted-foreground">Scored opportunities, best first. Nothing here is ever posted for you.</p>
      </header>
      <Inbox />
    </div>
  );
}
