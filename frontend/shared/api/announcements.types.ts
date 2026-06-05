export type AnnouncementDTO = {
  id: number;
  title: string;
  contentMarkdown: string;
  status: "active" | "inactive" | string;
  type: "critical" | "warning" | "info" | "normal" | "general" | string;
  pinned: boolean;
  priority: number;
  startsAt: string | null;
  expiresAt: string | null;
  createdByUserID: number;
  createdAt: string;
  updatedAt: string;
  closedAt: string | null;
};
