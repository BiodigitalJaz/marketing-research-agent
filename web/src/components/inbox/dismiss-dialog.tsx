"use client";

import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";

import { DISMISS_REASONS } from "./meta";

/** Dismissals require a reason code; they train future scoring. */
export function DismissDialog({ open, onCancel, onConfirm }: { open: boolean; onCancel: () => void; onConfirm: (reason: string, note: string) => void }) {
  const [reason, setReason] = useState("");
  const [note, setNote] = useState("");
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onCancel()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Why dismiss this?</DialogTitle>
          <DialogDescription>Your reason helps tune scoring. Press a number key to choose.</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => { e.preventDefault(); if (reason) onConfirm(reason, note); }}
          onKeyDown={(e) => {
            const n = Number(e.key);
            if (n >= 1 && n <= DISMISS_REASONS.length && (e.target as HTMLElement).tagName !== "INPUT") setReason(DISMISS_REASONS[n - 1].v);
          }}
        >
          <div className="grid grid-cols-2 gap-2">
            {DISMISS_REASONS.map((r, i) => (
              <Button key={r.v} type="button" variant={reason === r.v ? "default" : "outline"} onClick={() => setReason(r.v)} className="justify-start">
                <span className="text-muted-foreground">{i + 1}</span> {r.label}
              </Button>
            ))}
          </div>
          <Input aria-label="Note (optional)" placeholder="Note (optional)" value={note} onChange={(e) => setNote(e.target.value)} />
          <DialogFooter>
            <Button type="submit" disabled={!reason}>Dismiss</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
