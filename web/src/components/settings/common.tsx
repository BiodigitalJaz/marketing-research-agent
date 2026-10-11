"use client";

import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

export function Section({ title, description, actions, children }: { title: string; description?: string; actions?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold">{title}</h2>
          {description && <p className="text-xs text-muted-foreground">{description}</p>}
        </div>
        {actions}
      </div>
      {children}
    </section>
  );
}

export function Row({ id, label, hint, children, className }: { id: string; label: string; hint?: string; children: React.ReactNode; className?: string }) {
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

export function Check({ id, label, checked, onChange, disabled, hint }: { id: string; label: string; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; hint?: string }) {
  return (
    <label htmlFor={id} className={cn("flex items-start gap-2 text-sm", disabled && "opacity-50")}>
      <input id={id} type="checkbox" className="mt-0.5 size-4 accent-primary" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span>
        {label}
        {hint && <span className="block text-xs text-muted-foreground">{hint}</span>}
      </span>
    </label>
  );
}

export function Status({ ok, children }: { ok: boolean; children: React.ReactNode }) {
  return (
    <Badge variant={ok ? "secondary" : "destructive"} className="h-auto py-0.5 whitespace-normal">
      {children}
    </Badge>
  );
}

/** Parses a numeric input; empty string -> undefined. */
export function num(v: string): number | undefined {
  if (v.trim() === "") return undefined;
  const n = Number(v);
  return Number.isFinite(n) ? n : undefined;
}

export const TASKS = ["classify", "score", "draft", "research", "summarize"] as const;
/** Prompts that can be edited: every routed task, plus draft_check (runs on the draft route). */
export const PROMPT_TASKS = [...TASKS, "draft_check"] as const;
export const EFFORTS = ["low", "medium", "high", "xhigh", "max"] as const;
