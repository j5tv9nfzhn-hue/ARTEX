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

export interface StorageWriteQueue {
  /** 提交成功的事务批次数。 */
  batches: number;
  /** 入队并被处理过的写入总数（含回滚批里的）。 */
  jobs: number;
  max_batch: number;
  last_batch: number;
  /** 整批回滚次数：>0 说明有写入失败。 */
  rollbacks: number;
  /** 当前队列里还没被取走的写入数。 */
  queue_depth: number;
  /** jobs / batches：平均每批攒了几条。 */
  avg_batch: number;
  /** 一次写入从入队到提交完成的平均毫秒数（写 p99 的上界代理）。 */
  avg_wait_ms: number;
}

export interface StorageActivityRetention {
  /** 自动保留天数；0 = 关闭。 */
  days: number;
  last_run_unix_ms?: number;
  last_deleted: number;
  last_error?: string;
  interval_minutes: number;
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
  /** 单写协程的累计观测值。 */
  write_queue: StorageWriteQueue;
}

export interface StorageStats {
  db: StorageDB;
  mem: StorageMem;
  memory_db: StorageMemoryDB;
  activity_retention: StorageActivityRetention;
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
  /** 保存自动保留天数；0 = 关闭。 */
  activityRetention: (days: number) =>
    storageRequest<{ days: number; saved: boolean }>("/storage/activity/retention", post({ days })),
};
