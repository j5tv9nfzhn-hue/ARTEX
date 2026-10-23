"use client";

// 消息级回滚确认对话框（聊天页与任务总 AGENT 共用）：点击气泡下方的回滚箭头后
// 弹出，明示副作用。两种场景范围一致——只回退这段对话（隐藏该消息及其之后的对话
// 步骤、截断会话记录）；任务版额外说明「任务进度与 Worker/规划器记录不受影响」，
// 因为任务版过去会连任务一起回退，现在不会了。确认后由调用方执行回滚并把消息原文
// 回填输入框。文案在 messages/zh.json 的 "checkpoints.msgRollback" 命名空间。

import { TriangleAlertIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Spinner } from "@/components/ui/spinner";

export function MessageRollbackDialog({
  open,
  task,
  busy,
  onOpenChange,
  onConfirm,
}: {
  open: boolean;
  // task=true 用任务版说明（只回退主 Agent 这段对话，任务进度与执行记录不动）。
  task?: boolean;
  busy?: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
}) {
  const t = useTranslations("checkpoints.msgRollback");
  const tc = useTranslations("checkpoints");
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next && busy) return; // 回滚进行中不响应关闭
        onOpenChange(next);
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("title")}</AlertDialogTitle>
          <AlertDialogDescription>{task ? t("descTask") : t("descConversation")}</AlertDialogDescription>
        </AlertDialogHeader>
        <Alert>
          <TriangleAlertIcon />
          <AlertDescription>{task ? t("sideEffectsTask") : t("sideEffectsConversation")}</AlertDescription>
        </Alert>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{tc("cancel")}</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault();
              onConfirm();
            }}
          >
            {busy && <Spinner data-icon="inline-start" />}
            {busy ? tc("reverting") : tc("confirmRevert")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
