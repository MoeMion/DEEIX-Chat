import { AdminShell } from "@/features/admin/components/admin-shell";
import { AdminConversationSettingsPage } from "@/features/admin/components/sections/conversation/admin-conversation";

export default function AdminConversationSettingsRoute() {
  return (
    <AdminShell activeSection="conversation-settings" basePath="/admin">
      <AdminConversationSettingsPage />
    </AdminShell>
  );
}
