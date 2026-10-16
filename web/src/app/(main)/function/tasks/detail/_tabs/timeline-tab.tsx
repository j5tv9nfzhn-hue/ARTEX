"use client";

// 任务详情 · 时间线 tab（批次 6 / #32、#34）：检查点列表 + 回滚入口。
// 回滚要求任务已暂停；工具副作用与已落库的图产物不会回滚（对话框已明示）。

import { HistoryIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { CheckpointTimeline } from "@/components/checkpoint-timeline";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export function TimelineTab({ taskId, onReverted }: { taskId: string; onReverted?: () => void }) {
  const t = useTranslations("taskDetail.timeline");
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-sm">
          <HistoryIcon className="size-4" /> {t("title")}
        </CardTitle>
        <CardDescription>{t("description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <CheckpointTimeline scope="task" id={taskId} onReverted={onReverted} />
      </CardContent>
    </Card>
  );
}
