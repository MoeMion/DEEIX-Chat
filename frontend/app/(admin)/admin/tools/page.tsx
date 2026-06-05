import { AdminShell } from "@/features/admin/components/admin-shell";
import { AdminToolsPage } from "@/features/admin/components/sections/tools/admin-tools";

export default function AdminToolsRoute() {
  return (
    <AdminShell activeSection="tool-settings" basePath="/admin">
      <AdminToolsPage />
    </AdminShell>
  );
}
