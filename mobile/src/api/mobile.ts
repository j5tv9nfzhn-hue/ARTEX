import { apiFetch } from "./client";
import type {
  MobileAlertsPage,
  MobileOverview,
  MobileUsage,
  TaskItem,
} from "./types";

export const mobileApi = {
  overview: () => apiFetch<MobileOverview>("/api/mobile/overview"),

  alerts: (params: { limit?: number; sinceId?: number; level?: string; unreadOnly?: boolean } = {}) => {
    const query = new URLSearchParams();
    if (params.limit) query.set("limit", String(params.limit));
    if (params.sinceId) query.set("since_id", String(params.sinceId));
    if (params.level) query.set("level", params.level);
    if (params.unreadOnly) query.set("unread", "1");
    const suffix = query.toString() ? `?${query.toString()}` : "";
    return apiFetch<MobileAlertsPage>(`/api/mobile/alerts${suffix}`);
  },

  readAlerts: (ids: number[]) =>
    apiFetch<{ ok: boolean }>("/api/mobile/alerts/read", { method: "POST", body: { ids } }),

  readAllAlerts: () => apiFetch<{ ok: boolean }>("/api/mobile/alerts/read-all", { method: "POST" }),

  usage: (days = 7) => apiFetch<MobileUsage>(`/api/mobile/usage?days=${days}`),

  /** 桌面端 /api/tasks 的响应形状历来在数组与 {tasks:[…]} 之间摇摆，这里统一抹平。 */
  tasks: async (): Promise<TaskItem[]> => {
    const raw = await apiFetch<TaskItem[] | { tasks?: TaskItem[] }>("/api/tasks");
    if (Array.isArray(raw)) return raw;
    return raw.tasks ?? [];
  },

  task: (id: string) => apiFetch<TaskItem>(`/api/tasks/${encodeURIComponent(id)}`),

  /** 任务控制复用桌面端批量接口（只支持 pause / resume），手机端通常只传一条。
   *  注意：ARTEX 没有独立的 "stop"，暂停即停止继续执行——按钮文案与注释必须一致。 */
  controlTasks: (action: "pause" | "resume", taskIds: string[]) =>
    apiFetch<unknown>("/api/tasks/control/batch", {
      method: "POST",
      body: { action, task_ids: taskIds },
    }),

  registerPush: (payload: { platform: "ios" | "android"; token: string; device?: string }) =>
    apiFetch<{ ok: boolean }>("/api/mobile/push/register", { method: "POST", body: payload }),

  login: (password: string) =>
    apiFetch<{ token: string }>("/api/auth/login", {
      method: "POST",
      body: { username: "ARTEX", password },
    }),
};
