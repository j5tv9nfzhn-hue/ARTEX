export type CatalogTree = { [key: string]: string | CatalogTree };

// 递归增量合并，不能用新提取的顶层 namespace 覆盖旧树，否则已经改为 t(key)
// 的界面消息及人工维护键会被误删。默认保留已有译文，仅为新键添加中文回退。
export function mergeCatalogTree(existing: CatalogTree, additions: CatalogTree, overwrite = false): CatalogTree {
  const result: CatalogTree = { ...existing };
  for (const [key, value] of Object.entries(additions)) {
    const previous = existing[key];
    if (previous !== undefined && typeof previous !== typeof value) {
      throw new Error(`词库结构冲突：${key}，请人工确认后再提取。`);
    }
    if (typeof value === "string") {
      if (previous === undefined || overwrite) result[key] = value;
    } else {
      result[key] = mergeCatalogTree((previous as CatalogTree | undefined) ?? {}, value, overwrite);
    }
  }
  return result;
}
