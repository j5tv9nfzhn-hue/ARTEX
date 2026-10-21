/**
 * 工作空间根目录固定条目的用途说明与删除建议。
 *
 * 为什么写死在前端：这些目录由后端在固定路径下创建/维护
 * （tasks/ transcripts/ traces/ traffic/ sessions/ .delete-staging/），
 * 名字是约定而不是用户数据，前端只要能认出来就能给人一句人话解释，
 * 不必为此新增接口或往库里存一份元数据。
 *
 * 文案在 messages/workspace.hints.<key>.desc，这里只放映射与配色。
 */

export type WorkspaceHintTone = "keep" | "clean" | "caution";

export type WorkspaceHint = {
  /** i18n key：workspace.hints.<key>.desc */
  key: string;
  /** keep=建议保留 / clean=可安全删除 / caution=删除前先确认 */
  tone: WorkspaceHintTone;
};

/** 只在工作空间根目录生效：子目录里的同名文件夹不一定是系统目录。 */
export const workspaceRootHints: Record<string, WorkspaceHint> = {
  ".delete-staging": { key: "deleteStaging", tone: "clean" },
  sessions: { key: "sessions", tone: "keep" },
  tasks: { key: "tasks", tone: "keep" },
  traces: { key: "traces", tone: "clean" },
  traffic: { key: "traffic", tone: "caution" },
  transcripts: { key: "transcripts", tone: "keep" },
};

export function workspaceHintFor(name: string): WorkspaceHint | undefined {
  return workspaceRootHints[name];
}

export const workspaceToneClass: Record<WorkspaceHintTone, string> = {
  keep: "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
  clean: "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400",
  caution: "border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400",
};

export const workspaceToneLabelKey: Record<WorkspaceHintTone, string> = {
  keep: "toneKeep",
  clean: "toneClean",
  caution: "toneCaution",
};
