"use client";

import * as React from "react";

import { RotateCcwIcon, SendIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { resetNotifyPrefs, setNotifyPrefs, useNotifyPrefs } from "@/hooks/use-notify-prefs";
import { pushNotifyEvent } from "@/lib/notify-inbox";
import {
  clampDelaySec,
  clampTtlSec,
  DELAY_MAX_SEC,
  NOTIFY_TRIGGERS,
  type NotifyDock,
  type NotifySoundMode,
  type NotifyTriggerKey,
  type NotifyTtlMode,
  TTL_MAX_SEC,
  TTL_MIN_SEC,
} from "@/lib/notify-prefs";

// 站内通知的显示设置。
//
// 四项需求里的原始说法 → 这里的控件：
//   延迟时间            → delaySec（0 = 立即）
//   什么时候触发        → triggers 五个开关
//   声音               → sound 三档（关闭 / 仅严重 / 全部）
//   单条显示时长        → ttlMode + ttlSec（按级别自动，或统一固定值）
// 额外给了一个「落点」选择：默认右上角，就是为了不再压住页面右下角的创建按钮；
// 想放回右下角也留着，但明确标注了代价。

const DOCKS: readonly NotifyDock[] = ["top-right", "top-center", "bottom-right"];
const SOUND_MODES: readonly NotifySoundMode[] = ["all", "urgent", "off"];
const TTL_MODES: readonly NotifyTtlMode[] = ["auto", "fixed"];

export function NotifySettings() {
  const t = useTranslations("notifyPage");
  const prefs = useNotifyPrefs();

  // 数字输入用本地文本存一份：直接绑定 number 会让「清空重输」寸步难行
  // （空串一解析就回落默认值，光标位置也跟着跳）。提交时再夹到合法区间。
  const [delayText, setDelayText] = React.useState(String(prefs.delaySec));
  const [ttlText, setTtlText] = React.useState(String(prefs.ttlSec));
  React.useEffect(() => setDelayText(String(prefs.delaySec)), [prefs.delaySec]);
  React.useEffect(() => setTtlText(String(prefs.ttlSec)), [prefs.ttlSec]);

  const toggleTrigger = (key: NotifyTriggerKey, on: boolean) => {
    setNotifyPrefs({ triggers: { ...prefs.triggers, [key]: on } });
  };

  const sendTest = () => {
    // 负数 id 是最省事的「不是后端事件」标记，不会和 SSE 的自增 id 撞。
    const id = -Date.now();
    pushNotifyEvent({
      id,
      ts: Date.now(),
      kind: "task_done",
      level: "success",
      title: t("settings.testTitle"),
      body: t("settings.testBody"),
    });
  };

  return (
    <Card className="gap-3">
      <CardHeader>
        <CardTitle className="text-base">{t("settings.title")}</CardTitle>
      </CardHeader>
      <CardContent className="grid gap-4">
        <p className="text-muted-foreground text-xs leading-5">{t("settings.subtitle")}</p>

        {/* 延迟 */}
        <div className="grid gap-2">
          <Label htmlFor="n-delay">{t("settings.delay")}</Label>
          <div className="flex items-center gap-2">
            <Input
              id="n-delay"
              type="number"
              min={0}
              max={DELAY_MAX_SEC}
              inputMode="numeric"
              className="w-24 tabular-nums"
              value={delayText}
              onChange={(e) => {
                setDelayText(e.target.value);
                const n = Number(e.target.value);
                if (e.target.value.trim() !== "" && Number.isFinite(n)) {
                  setNotifyPrefs({ delaySec: clampDelaySec(n) });
                }
              }}
              onBlur={() => setDelayText(String(prefs.delaySec))}
            />
            <span className="text-muted-foreground text-xs">{t("settings.unitSec")}</span>
          </div>
          <p className="text-muted-foreground text-xs">{t("settings.delayHint")}</p>
        </div>

        {/* 单条显示时长 */}
        <div className="grid gap-2">
          <Label>{t("settings.ttl")}</Label>
          <div className="flex items-center gap-2">
            <Select value={prefs.ttlMode} onValueChange={(v) => setNotifyPrefs({ ttlMode: v as NotifyTtlMode })}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {TTL_MODES.map((m) => (
                  <SelectItem key={m} value={m}>
                    {t(`settings.ttlMode.${m}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {prefs.ttlMode === "fixed" ? (
              <>
                <Input
                  type="number"
                  min={TTL_MIN_SEC}
                  max={TTL_MAX_SEC}
                  inputMode="numeric"
                  aria-label={t("settings.ttl")}
                  className="w-24 tabular-nums"
                  value={ttlText}
                  onChange={(e) => {
                    setTtlText(e.target.value);
                    const n = Number(e.target.value);
                    if (e.target.value.trim() !== "" && Number.isFinite(n)) {
                      setNotifyPrefs({ ttlSec: clampTtlSec(n) });
                    }
                  }}
                  onBlur={() => setTtlText(String(prefs.ttlSec))}
                />
                <span className="text-muted-foreground text-xs">{t("settings.unitSec")}</span>
              </>
            ) : null}
          </div>
          <p className="text-muted-foreground text-xs">{t("settings.ttlHint")}</p>
        </div>

        {/* 声音 */}
        <div className="grid gap-2">
          <Label>{t("settings.sound")}</Label>
          <Select value={prefs.sound} onValueChange={(v) => setNotifyPrefs({ sound: v as NotifySoundMode })}>
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SOUND_MODES.map((m) => (
                <SelectItem key={m} value={m}>
                  {t(`settings.soundMode.${m}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-muted-foreground text-xs">{t("settings.soundHint")}</p>
        </div>

        {/* 触发条件 */}
        <div className="grid gap-2">
          <Label>{t("settings.triggers")}</Label>
          <div className="grid gap-2">
            {NOTIFY_TRIGGERS.map((key) => (
              <div key={key} className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="text-sm">{t(`settings.trigger.${key}`)}</div>
                  <p className="text-muted-foreground text-xs leading-4">{t(`settings.triggerHint.${key}`)}</p>
                </div>
                <Switch
                  checked={prefs.triggers[key] !== false}
                  onCheckedChange={(on) => toggleTrigger(key, on)}
                  aria-label={t(`settings.trigger.${key}`)}
                />
              </div>
            ))}
          </div>
          <p className="text-muted-foreground text-xs">{t("settings.triggersHint")}</p>
        </div>

        {/* 落点 */}
        <div className="grid gap-2">
          <Label>{t("settings.dock")}</Label>
          <Select value={prefs.dock} onValueChange={(v) => setNotifyPrefs({ dock: v as NotifyDock })}>
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {DOCKS.map((d) => (
                <SelectItem key={d} value={d}>
                  {t(`settings.dockMode.${d}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-muted-foreground text-xs">{t("settings.dockHint")}</p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={sendTest}>
            <SendIcon />
            {t("settings.test")}
          </Button>
          <Button variant="ghost" size="sm" onClick={resetNotifyPrefs}>
            <RotateCcwIcon />
            {t("settings.reset")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
