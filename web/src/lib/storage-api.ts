import { http } from "@/lib/api";
import { MOCK } from "@/lib/mock/enabled";

// 存储与清理页的数据访问层。
//
// 与 retention-api 同样的取舍：维护类页面绝不走 mock 假数据——那是唯一会
// 真的删东西的界面，演示数据会让人误以为按钮生效了。

export interface StorageTableRow {
  name: string;
  rows: number;
}

export interface StorageDB {
  path: string;
  size: number;
  wal_size: number;
  shm_size: number;
  page_size: number;
  page_count: number;
  freelist: number;
  free_bytes: number;
  used_bytes: number;
  auto_vacuum: string;
  journal_mode: string;
  tables: StorageTableRow[];
  tables_partial: boolean;
  tables_total: number;
}

export interface StorageMem {
  heap_alloc: number;
  heap_inuse: number;
  heap_sys: number;
  stack_inuse: number;
  sys: number;
  num_gc: number;
  last_gc_unix_ms: number;
  goroutines: number;
  notify_ring: number;
  notify_ring_cap: number;
  task_handles: number;
  http_transports: number;
}

export interface StorageMemoryDB {
  facts: number;
  edges: number;
  activity: number;
  findings: number;
  assets: number;
  links: number;
  orphan_facts: number;
  orphan_edges: number;
  orphan_activity: number;
  /** 30 天前的活动行数：只用于判断「活动清理」能腾出多少。 */
  activity_older_30d: number;
}

export interface StorageStats {
  db: StorageDB;
  mem: StorageMem;
  memory_db: StorageMemoryDB;
}

export interface CompactResult {
  before: number;
  after: number;
  freed: number;
  duration_ms: number;
  wal_size: number;
}

export interface GcResult {
  heap_before: number;
  heap_after: number;
  sys_before: number;
  sys_after: number;
  goroutines: number;
}

export interface OrphanCounts {
  facts: number;
  edges: number;
  activity: number;
}

export interface OrphanPreview {
  counts: OrphanCounts;
  deleted: boolean;
}

export interface PurgeResult {
  deleted: boolean;
  rows?: OrphanCounts | number;
  freed?: number;
}

export async function storageRequest<T>(path: string, init?: RequestInit): Promise<T> {
  if (MOCK) throw new Error("storage_mock_unsupported");
  return http<T>(path, { cache: "no-store", credentials: "same-origin", ...init });
}

const post = (body: object): RequestInit => ({ method: "POST", body: JSON.stringify(body) });

export const storageApi = {
  stats: () => storageRequest<StorageStats>("/storage/stats"),
  compact: () => storageRequest<CompactResult>("/storage/compact", post({})),
  gc: () => storageRequest<GcResult>("/storage/gc", post({})),
  clearNotify: () => storageRequest<{ cleared: number }>("/storage/notify/clear", post({})),
  orphanPreview: () => storageRequest<OrphanPreview>("/storage/orphans/purge", post({ dry_run: true })),
  orphanPurge: () => storageRequest<PurgeResult>("/storage/orphans/purge", post({ dry_run: false, confirm: true })),
  activityPreview: (days: number) =>
    storageRequest<{ days: number; rows: number; deleted: boolean }>(
      "/storage/activity/purge",
      post({ dry_run: true, days }),
    ),
  activityPurge: (days: number) =>
    storageRequest<PurgeResult>("/storage/activity/purge", post({ dry_run: false, confirm: true, days })),
};
