"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { errorMessage } from "@/lib/api/client";

/** Wraps a mutation that invalidates `key` and toasts the outcome. */
export function useCrudMutation<TVars, TRes>(key: readonly unknown[], fn: (v: TVars) => Promise<TRes>, successMsg: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: key });
      toast.success(successMsg);
    },
    onError: (err) => toast.error(errorMessage(err)),
  });
}
