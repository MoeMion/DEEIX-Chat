import { authedRequest } from "@/shared/api/authed-client";
import type { AnnouncementDTO } from "@/shared/api/announcements.types";

export async function listAnnouncements(accessToken: string): Promise<AnnouncementDTO[]> {
  return authedRequest<AnnouncementDTO[]>("/api/v1/announcements", { accessToken }, true);
}

export async function dismissAnnouncementToday(accessToken: string, announcementID: number, updatedAt: string): Promise<void> {
  await authedRequest<{ dismissed: boolean }>(
    `/api/v1/announcements/${encodeURIComponent(String(announcementID))}/dismiss-today`,
    { method: "POST", accessToken, body: { updatedAt } },
    true,
  );
}

export async function closeAnnouncement(accessToken: string, announcementID: number, updatedAt: string): Promise<void> {
  await authedRequest<{ closed: boolean }>(
    `/api/v1/announcements/${encodeURIComponent(String(announcementID))}/close`,
    { method: "POST", accessToken, body: { updatedAt } },
    true,
  );
}
