import type { UserSettingsMap } from "@/shared/api/user-settings";

export const USER_SETTINGS_UPDATED_EVENT = "deeix-chat:user-settings-updated";

export function dispatchUserSettingsUpdated(settings: UserSettingsMap) {
  if (typeof window === "undefined") {
    return;
  }
  window.dispatchEvent(new CustomEvent<UserSettingsMap>(USER_SETTINGS_UPDATED_EVENT, { detail: settings }));
}
