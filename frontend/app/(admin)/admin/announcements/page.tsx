import { AdminShell } from "@/features/admin/components/admin-shell";
import { AdminAnnouncementsPage } from "@/features/admin/components/sections/announcements/admin-announcements";

export default function AdminAnnouncementsRoute() {
  return (
    <AdminShell activeSection="announcements" basePath="/admin">
      <AdminAnnouncementsPage />
    </AdminShell>
  );
}
