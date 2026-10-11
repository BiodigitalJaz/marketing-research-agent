"use client";

import { XIcon } from "lucide-react";
import { useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";

type Props = {
  id?: string;
  value: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
  disabled?: boolean;
};

/** Editable list of short strings: type and press Enter (or paste a comma list) to add. */
export function ListInput({ id, value, onChange, placeholder, disabled }: Props) {
  const [draft, setDraft] = useState("");

  function add(raw: string) {
    const parts = raw
      .split(/[,\n]/)
      .map((s) => s.trim())
      .filter(Boolean);
    const lower = new Set(value.map((v) => v.toLowerCase()));
    const next = [...value];
    for (const p of parts) {
      if (!lower.has(p.toLowerCase())) {
        next.push(p);
        lower.add(p.toLowerCase());
      }
    }
    onChange(next);
    setDraft("");
  }

  return (
    <div className="flex flex-col gap-2">
      {value.length > 0 && (
        <ul className="flex flex-wrap gap-1.5">
          {value.map((v) => (
            <li key={v}>
              <Badge variant="secondary" className="h-auto max-w-full gap-1 py-0.5 whitespace-normal">
                <span className="break-words">{v}</span>
                {!disabled && (
                  <button
                    type="button"
                    aria-label={`Remove ${v}`}
                    className="rounded-sm opacity-60 hover:opacity-100"
                    onClick={() => onChange(value.filter((x) => x !== v))}
                  >
                    <XIcon className="size-3" />
                  </button>
                )}
              </Badge>
            </li>
          ))}
        </ul>
      )}
      {!disabled && (
        <Input
          id={id}
          value={draft}
          placeholder={placeholder ?? "Type and press Enter"}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              if (draft.trim()) add(draft);
            }
          }}
          onBlur={() => draft.trim() && add(draft)}
        />
      )}
    </div>
  );
}
