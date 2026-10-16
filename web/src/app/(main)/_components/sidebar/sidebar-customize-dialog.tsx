"use client";

import * as React from "react";

import { Eye, EyeOff, Pin, PinOff, RotateCcw, SlidersHorizontal } from "lucide-react";
import { useTranslations } from "next-intl";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem } from "@/components/ui/sidebar";
import {
  resetSidebarPrefs,
  toggleSidebarItemHidden,
  toggleSidebarItemPinned,
  useSidebarPrefs,
} from "@/lib/sidebar-prefs";
import { cn } from "@/lib/utils";
import { navGroupMessageKey, sidebarItems } from "@/navigation/sidebar/sidebar-items";

// SidebarCustomize 是侧边栏底部的入口：一个菜单按钮 + 自定义对话框。
// 偏好（置顶/隐藏）通过 sidebar-prefs 持久化到 localStorage，改动即时反映到侧边栏。
export function SidebarCustomize() {
  const t = useTranslations("nav");
  const [open, setOpen] = React.useState(false);

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <SidebarMenuButton tooltip={t("customize.trigger")} onClick={() => setOpen(true)}>
          <SlidersHorizontal />
          <span>{t("customize.trigger")}</span>
        </SidebarMenuButton>
      </SidebarMenuItem>
      <SidebarCustomizeDialog open={open} onOpenChange={setOpen} />
    </SidebarMenu>
  );
}

function SidebarCustomizeDialog({
  open,
  onOpenChange,
}: {
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
}) {
  const t = useTranslations("nav");
  const prefs = useSidebarPrefs();
  const pinnedSet = new Set(prefs.pinned);
  const hiddenSet = new Set(prefs.hidden);

  const groupLabel = (id: number, fallback?: string) => {
    const key = navGroupMessageKey(id);
    return t.has(key) ? t(key) : (fallback ?? "");
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("customize.title")}</DialogTitle>
          <DialogDescription>{t("customize.description")}</DialogDescription>
        </DialogHeader>
        <div className="flex max-h-[60vh] flex-col gap-4 overflow-y-auto py-1">
          {sidebarItems.map((group) => (
            <div key={group.id} className="flex flex-col gap-1">
              <span className="text-muted-foreground px-1 text-xs font-medium">
                {groupLabel(group.id, group.label)}
              </span>
              {group.items.map((item) => {
                const pinned = pinnedSet.has(item.id);
                const hidden = hiddenSet.has(item.id);
                const title = t.has(`item.${item.id}`) ? t(`item.${item.id}`) : item.title;
                return (
                  <div
                    key={item.id}
                    className={cn(
                      "flex items-center gap-2 rounded-md border px-2 py-1.5 text-sm",
                      hidden && "opacity-50",
                    )}
                  >
                    <span className="truncate">{title}</span>
                    <div className="ml-auto flex shrink-0 items-center gap-1">
                      <Button
                        type="button"
                        variant={pinned ? "secondary" : "ghost"}
                        size="icon-xs"
                        aria-pressed={pinned}
                        aria-label={pinned ? t("customize.unpin") : t("customize.pin")}
                        title={pinned ? t("customize.unpin") : t("customize.pin")}
                        onClick={() => toggleSidebarItemPinned(item.id, !pinned)}
                      >
                        {pinned ? <PinOff /> : <Pin />}
                      </Button>
                      <Button
                        type="button"
                        variant={hidden ? "secondary" : "ghost"}
                        size="icon-xs"
                        aria-pressed={hidden}
                        aria-label={hidden ? t("customize.unhide") : t("customize.hide")}
                        title={hidden ? t("customize.unhide") : t("customize.hide")}
                        onClick={() => toggleSidebarItemHidden(item.id, !hidden)}
                      >
                        {hidden ? <EyeOff /> : <Eye />}
                      </Button>
                    </div>
                  </div>
                );
              })}
            </div>
          ))}
        </div>
        <DialogFooter className="sm:justify-between">
          <Button type="button" variant="outline" onClick={() => resetSidebarPrefs()}>
            <RotateCcw data-icon="inline-start" />
            {t("customize.reset")}
          </Button>
          <Button type="button" onClick={() => onOpenChange(false)}>
            {t("customize.close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
