import { Label } from "@/components/ui/label";

export function Field({ id, label, hint, children }: { id: string; label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      {children}
    </div>
  );
}

export function EmbeddedBadge({ embedded }: { embedded: boolean }) {
  return embedded ? (
    <span className="text-xs text-emerald-600 dark:text-emerald-400">Embedded</span>
  ) : (
    <span className="text-xs text-muted-foreground" title="The embedding is computed in the background">
      Embedding pending
    </span>
  );
}
