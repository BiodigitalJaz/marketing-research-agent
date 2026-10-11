"use client";

import { useQuery } from "@tanstack/react-query";

import { api } from "@/lib/api/client";

export function ApiStatus() {
  const { data, isPending, isError } = useQuery({
    queryKey: ["ready"],
    queryFn: async () => {
      const { data, error } = await api.GET("/ready");
      if (error) throw new Error(error.error.message);
      return data;
    },
    refetchInterval: 15_000,
  });

  const [label, color] = isPending
    ? ["Checking API…", "bg-muted-foreground"]
    : isError
      ? ["API unavailable", "bg-destructive"]
      : [`API ${data.status}`, "bg-emerald-500"];

  return (
    <p className="flex items-center gap-2 text-xs text-muted-foreground" role="status">
      <span className={`inline-block size-2 rounded-full ${color}`} aria-hidden />
      {label}
    </p>
  );
}
