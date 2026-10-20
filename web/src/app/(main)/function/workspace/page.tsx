"use client";

import * as React from "react";

import {
  ArchiveIcon,
  DownloadIcon,
  FileIcon,
  FolderIcon,
  FolderPlusIcon,
  HardDriveIcon,
  RefreshCwIcon,
  SaveIcon,
  Trash2Icon,
  TriangleAlertIcon,
  UploadIcon,
} from "lucide-react";
import { useTranslations } from "next-intl";
import { toast } from "sonner";

import { EmptyState } from "@/components/empty-state";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { WorkspaceEntry, WorkspaceFile } from "@/lib/types";
import { cn } from "@/lib/utils";

function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}
function fmtTime(ms: number): string {
  return new Date(ms).toLocaleString("zh-CN", {
    year: "2-digit",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

type EditState = {
  file: WorkspaceFile;
  content: string;
  dirty: boolean;
  saving: boolean;
};

export default function WorkspacePage() {
  const t = useTranslations("workspace");
  const [path, setPath] = React.useState("");
  const [entries, setEntries] = React.useState<WorkspaceEntry[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [edit, setEdit] = React.useState<EditState | null>(null);
  const [mkdirOpen, setMkdirOpen] = React.useState(false);
  const [mkdirName, setMkdirName] = React.useState("");
  const uploadRef = React.useRef<HTMLInputElement>(null);
  // 当前目录的合计占用。之前的界面只在表格里列单条，看不出「这一层到底多大」。
  const [summary, setSummary] = React.useState({ size: 0, files: 0, dirs: 0, partial: false });
  // 删除确认：把「删什么、占多大、要不要先备份」摊开写清楚，存储敏感的用户才敢
  // 动手；目录额外给一条「仅清空内容」的保底选项。
  const [pending, setPending] = React.useState<{
    entry: WorkspaceEntry;
    backupFirst: boolean;
    busy: boolean;
  } | null>(null);

  const load = React.useCallback(
    (p: string) => {
      setLoading(true);
      api
        .workspaceList(p)
        .then((r) => {
          setEntries(r.entries);
          setPath(r.path);
          setSummary({
            size: r.total_size ?? 0,
            files: r.total_files ?? 0,
            dirs: r.total_dirs ?? 0,
            partial: r.total_partial ?? false,
          });
        })
        .catch((e) => toast.error(t("readDirFailed", { msg: (e as Error).message })))
        .finally(() => setLoading(false));
    },
    [t],
  );

  React.useEffect(() => {
    load("");
  }, [load]);

  const crumbs = React.useMemo(() => {
    const parts = path ? path.split("/") : [];
    const acc: { name: string; path: string }[] = [{ name: t("crumbRoot"), path: "" }];
    let cur = "";
    for (const part of parts) {
      cur = cur ? `${cur}/${part}` : part;
      acc.push({ name: part, path: cur });
    }
    return acc;
  }, [path, t]);

  const openFile = (e: WorkspaceEntry) => {
    api
      .workspaceRead(e.path)
      .then((f) => setEdit({ file: f, content: f.content ?? "", dirty: false, saving: false }))
      .catch((err) => toast.error(t("openFileFailed", { msg: (err as Error).message })));
  };

  const saveFile = () => {
    if (!edit) return;
    setEdit({ ...edit, saving: true });
    api
      .workspaceWrite(edit.file.path, edit.content)
      .then(() => {
        toast.success(t("saved"));
        setEdit((cur) => (cur ? { ...cur, dirty: false, saving: false } : cur));
        load(path);
      })
      .catch((err) => {
        toast.error(t("saveFailed", { msg: (err as Error).message }));
        setEdit((cur) => (cur ? { ...cur, saving: false } : cur));
      });
  };

  const askDelete = (e: WorkspaceEntry) => setPending({ entry: e, backupFirst: false, busy: false });

  // contents=true 走「仅清空内容」，false 走整条删除。
  const runDelete = async (contents: boolean) => {
    if (!pending) return;
    const { entry, backupFirst } = pending;
    setPending({ ...pending, busy: true });
    try {
      // 备份先于删除：ZIP 是流式下载，等它开始传输再删，顺序不会颠倒。
      if (backupFirst) await api.workspaceBackup(entry.path);
      const r = await api.workspaceDelete(entry.path, contents);
      toast.success(t("freedSpace", { size: fmtSize(r.freed ?? entry.size) }));
      setPending(null);
      load(path);
    } catch (err) {
      toast.error(t("deleteFailed", { msg: (err as Error).message }));
      setPending((cur) => (cur ? { ...cur, busy: false } : cur));
    }
  };

  const doBackup = (e: WorkspaceEntry) =>
    api
      .workspaceBackup(e.path)
      .then(() => toast.success(t("backupDone", { name: e.name })))
      .catch((err) => toast.error(t("backupFailed", { msg: (err as Error).message })));

  const doUpload = (files: FileList | null) => {
    if (!files || files.length === 0) return;
    api
      .workspaceUpload(path, Array.from(files))
      .then((r) => {
        toast.success(t("uploaded", { n: r.uploaded }));
        load(path);
      })
      .catch((err) => toast.error(t("uploadFailed", { msg: (err as Error).message })))
      .finally(() => {
        if (uploadRef.current) uploadRef.current.value = "";
      });
  };

  const doMkdir = () => {
    const name = mkdirName.trim();
    if (!name) return;
    const target = path ? `${path}/${name}` : name;
    api
      .workspaceMkdir(target)
      .then(() => {
        toast.success(t("folderCreated"));
        setMkdirOpen(false);
        setMkdirName("");
        load(path);
      })
      .catch((err) => toast.error(t("createFailed", { msg: (err as Error).message })));
  };

  return (
    <div className="flex flex-col gap-4">
      {/* 页头：路径面包屑与操作。 */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-1 text-sm">
          <HardDriveIcon className="text-muted-foreground mr-1 size-4 shrink-0" />
          {crumbs.map((c, i) => (
            <React.Fragment key={c.path}>
              {i > 0 && <span className="text-muted-foreground">/</span>}
              <button
                type="button"
                onClick={() => load(c.path)}
                className={cn(
                  "max-w-[160px] truncate rounded px-1.5 py-0.5 hover:bg-accent",
                  i === crumbs.length - 1 ? "text-foreground font-medium" : "text-muted-foreground",
                )}
              >
                {c.name}
              </button>
            </React.Fragment>
          ))}
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => setMkdirOpen(true)}>
            <FolderPlusIcon /> {t("newFolder")}
          </Button>
          <Button variant="outline" size="sm" onClick={() => uploadRef.current?.click()}>
            <UploadIcon /> {t("upload")}
          </Button>
          <Button variant="ghost" size="icon" className="size-8" onClick={() => load(path)} title={t("refresh")}>
            <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} />
          </Button>
          <input ref={uploadRef} type="file" multiple className="hidden" onChange={(e) => doUpload(e.target.files)} />
        </div>
      </div>

      {/* 当前目录合计：一眼看出这层占了多少、多少个文件。 */}
      <div className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
        <span className="text-foreground font-mono text-sm">
          {summary.partial ? "≥ " : ""}
          {fmtSize(summary.size)}
        </span>
        <span>·</span>
        <span>{t("summary", { files: summary.files, dirs: summary.dirs })}</span>
      </div>

      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("colName")}</TableHead>
                <TableHead className="w-28 text-right">{t("colSize")}</TableHead>
                <TableHead className="w-40">{t("colMtime")}</TableHead>
                <TableHead className="w-24 text-right">{t("colActions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {entries.length === 0 && (
                <TableRow>
                  <TableCell colSpan={4} className="p-0">
                    {/* 加载态与空态拆开:加载中显示文案,加载完成仍为空才给空态插画 */}
                    {loading ? (
                      <div className="text-muted-foreground py-10 text-center text-sm">{t("loading")}</div>
                    ) : (
                      <EmptyState illustration="terminal" title={t("empty")} />
                    )}
                  </TableCell>
                </TableRow>
              )}
              {entries.map((e) => (
                <TableRow key={e.path}>
                  <TableCell>
                    <button
                      type="button"
                      onClick={() => (e.dir ? load(e.path) : openFile(e))}
                      className="flex items-center gap-2 text-left hover:underline"
                    >
                      {e.dir ? (
                        <FolderIcon className="size-4 shrink-0 text-blue-500" />
                      ) : (
                        <FileIcon className="text-muted-foreground size-4 shrink-0" />
                      )}
                      <span className="truncate font-mono text-sm">{e.name}</span>
                    </button>
                  </TableCell>
                  <TableCell
                    className="text-muted-foreground text-right font-mono text-xs"
                    title={e.size_partial ? t("sizePartial") : undefined}
                  >
                    {/* 目录显示子树累计占用；没统计完的加「≥」，不假装精确。 */}
                    {e.size_partial ? "≥ " : ""}
                    {fmtSize(e.size)}
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs">{fmtTime(e.mtime)}</TableCell>
                  <TableCell>
                    <div className="flex items-center justify-end gap-1">
                      {/* 目录也能备份：整棵子树打成 ZIP，删之前先留一份。 */}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-7"
                        title={t("backup")}
                        onClick={() => doBackup(e)}
                      >
                        <ArchiveIcon className="size-3.5" />
                      </Button>
                      {!e.dir && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-7"
                          title={t("download")}
                          onClick={() =>
                            api
                              .workspaceDownload(e.path)
                              .catch((err) => toast.error(t("downloadFailed", { msg: (err as Error).message })))
                          }
                        >
                          <DownloadIcon className="size-3.5" />
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="text-destructive size-7"
                        title={t("delete")}
                        onClick={() => askDelete(e)}
                      >
                        <Trash2Icon className="size-3.5" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {/* 查看、编辑文件。 */}
      <Sheet open={edit !== null} onOpenChange={(o) => !o && setEdit(null)}>
        <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 sm:max-w-2xl">
          {edit && (
            <>
              <SheetHeader className="border-b p-4">
                <SheetTitle className="flex items-center gap-2 truncate font-mono text-sm">
                  <FileIcon className="size-4 shrink-0" />
                  <span className="truncate" title={edit.file.path}>
                    {edit.file.path}
                  </span>
                </SheetTitle>
                <span className="text-muted-foreground text-xs">{fmtSize(edit.file.size)}</span>
              </SheetHeader>

              {edit.file.binary || edit.file.too_large ? (
                <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center">
                  <p className="text-muted-foreground text-sm">{edit.file.too_large ? t("tooLarge") : t("binary")}</p>
                  <Button variant="outline" onClick={() => api.workspaceDownload(edit.file.path)}>
                    <DownloadIcon /> {t("downloadFile")}
                  </Button>
                </div>
              ) : (
                <>
                  <div className="min-h-0 flex-1 p-3">
                    <Textarea
                      value={edit.content}
                      onChange={(ev) => setEdit({ ...edit, content: ev.target.value, dirty: true })}
                      spellCheck={false}
                      className="h-full min-h-[50vh] resize-none font-mono text-xs leading-relaxed"
                    />
                  </div>
                  <SheetFooter className="flex-row items-center justify-between border-t p-3">
                    <span className="text-muted-foreground text-xs">{edit.dirty ? t("dirty") : t("synced")}</span>
                    <div className="flex gap-2">
                      <Button variant="outline" onClick={() => api.workspaceDownload(edit.file.path)}>
                        <DownloadIcon /> {t("download")}
                      </Button>
                      <Button onClick={saveFile} disabled={!edit.dirty || edit.saving}>
                        <SaveIcon /> {edit.saving ? t("saving") : t("save")}
                      </Button>
                    </div>
                  </SheetFooter>
                </>
              )}
            </>
          )}
        </SheetContent>
      </Sheet>

      {/* 删除确认：类型、大小、文件数、是否先备份，全部摊开。 */}
      <Dialog open={pending !== null} onOpenChange={(o) => !o && !pending?.busy && setPending(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <TriangleAlertIcon className="text-destructive size-4" />
              {t("deleteTitle")}
            </DialogTitle>
          </DialogHeader>
          {pending && (
            <div className="flex flex-col gap-3 text-sm">
              <p className="break-all">
                {pending.entry.dir
                  ? t("deleteDirIntro", {
                      name: pending.entry.name,
                      files: pending.entry.files ?? 0,
                      size: fmtSize(pending.entry.size),
                    })
                  : t("deleteFileIntro", {
                      name: pending.entry.name,
                      size: fmtSize(pending.entry.size),
                    })}
              </p>
              {pending.entry.dir && <p className="text-muted-foreground text-xs">{t("deleteContentsIntro")}</p>}
              <label className="flex items-center gap-2 text-xs">
                <input
                  type="checkbox"
                  checked={pending.backupFirst}
                  onChange={(ev) => setPending({ ...pending, backupFirst: ev.target.checked })}
                  className="size-3.5"
                />
                {t("backupFirst")}
              </label>
            </div>
          )}
          <DialogFooter className="flex-wrap gap-2">
            <Button variant="outline" onClick={() => setPending(null)} disabled={pending?.busy}>
              {t("cancel")}
            </Button>
            {pending?.entry.dir ? (
              <>
                <Button variant="outline" onClick={() => runDelete(true)} disabled={pending.busy}>
                  {t("deleteContents")}
                </Button>
                <Button variant="destructive" onClick={() => runDelete(false)} disabled={pending.busy}>
                  {pending.busy ? t("deleting") : t("deleteWhole")}
                </Button>
              </>
            ) : (
              <Button variant="destructive" onClick={() => runDelete(false)} disabled={pending?.busy}>
                {pending?.busy ? t("deleting") : t("delete")}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 新建文件夹。 */}
      <Dialog open={mkdirOpen} onOpenChange={setMkdirOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>{t("newFolder")}</DialogTitle>
          </DialogHeader>
          <Input
            autoFocus
            value={mkdirName}
            onChange={(e) => setMkdirName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && doMkdir()}
            placeholder={t("folderNamePlaceholder")}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setMkdirOpen(false)}>
              {t("cancel")}
            </Button>
            <Button onClick={doMkdir} disabled={!mkdirName.trim()}>
              {t("create")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
