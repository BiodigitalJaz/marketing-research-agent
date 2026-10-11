import { AppShell } from "@/components/app-shell";
import { SessionGate } from "@/lib/session";

export default function AppLayout({ children }: LayoutProps<"/">) {
  return (
    <SessionGate>
      <AppShell>{children}</AppShell>
    </SessionGate>
  );
}
