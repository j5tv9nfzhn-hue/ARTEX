/** 与 server/mobile_api.go 的 DTO 一一对应。字段名保持 snake_case，避免两套命名来回翻译。 */

export type MobileTaskBrief = {
  id: string;
  name?: string;
  goal?: string;
  paused: boolean;
  queued: boolean;
  created_at?: number;
};

export type MobileTokenTotals = {
  input: number;
  output: number;
  cache_read: number;
};

export type MobileOverview = {
  server_time: number;
  engine_mode: "idle" | "running" | "paused" | string;
  llm_ready: boolean;
  active_task?: MobileTaskBrief;
  running: number;
  queued: number;
  paused: number;
  alerts_unread: number;
  alerts_by_level: Record<string, number>;
  tokens_today: MobileTokenTotals;
  assets: number;
  traffic: number;
};

export type MobileAlert = {
  id: number;
  ts: number;
  level: "error" | "warn" | "success" | "info" | string;
  kind: string;
  severity?: string;
  title: string;
  body?: string;
  href?: string;
  task_id?: string;
  read_at?: number;
};

export type MobileAlertsPage = {
  server_time: number;
  items: MobileAlert[];
  unread: number;
  by_level: Record<string, number>;
};

export type MobileUsageDay = {
  date: string;
  input: number;
  output: number;
  cache_read: number;
};

export type MobileProfileUsage = {
  profile: string;
  input: number;
  output: number;
  cache_read: number;
};

export type MobileUsage = {
  days: number;
  total: MobileTokenTotals;
  daily: MobileUsageDay[];
  by_profile: MobileProfileUsage[];
};

/** 任务列表沿用桌面端 /api/tasks 的 DTO（只取手机端需要的字段）。 */
export type TaskItem = {
  id: string;
  name?: string;
  goal?: string;
  description?: string;
  paused?: boolean;
  queued?: boolean;
  created_at?: number;
  completed_at?: number;
};
