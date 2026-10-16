"use client";

import * as React from "react";

import { ArrowDownIcon, ArrowUpIcon, XIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { Button } from "@/components/ui/button";
import {
  Combobox,
  ComboboxChip,
  ComboboxChips,
  ComboboxChipsInput,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxItem,
  ComboboxList,
  ComboboxValue,
} from "@/components/ui/combobox";
import { ChainCapabilityAdvice, ProfileCapabilityBadges } from "@/components/llm-profile-badges";
import type { AgentLLMProfileOption } from "@/lib/types";

// AgentLLMFallbackChain 编辑「某个角色」的备用模型链尾（计划 #139/#144）。
// 与任务链编辑器（TaskLLMProfileChain）的区别：
//   - 链首是上方「默认模型」绑定的固定主模型（agents.llm_profile_id），不在这里选；
//     未绑定时备用链不生效，但允许先把顺序配好，绑定后立即生效；
//   - 没有「当前游标」概念：角色链按 position 顺序依次故障转移，链首恒为第一位，
//     全部失败即报告错误，不会改写任务的模型链，也不影响其他角色的模型与熔断。
interface AgentLLMFallbackChainProps {
  profiles: AgentLLMProfileOption[];
  /** 备用链尾（按顺序的 profile id 字符串） */
  value: string[];
  onValueChange: (value: string[]) => void;
  /** 链首（主绑定）profile id 字符串；空 = 未绑定 */
  headProfileId?: string;
  disabled?: boolean;
  inputId?: string;
}

export function AgentLLMFallbackChain({
  profiles,
  value,
  onValueChange,
  headProfileId = "",
  disabled = false,
  inputId,
}: AgentLLMFallbackChainProps) {
  const t = useTranslations("llmChain");
  const profilesByID = React.useMemo(() => new Map(profiles.map((profile) => [String(profile.id), profile])), [profiles]);
  // 链首不出现在备用链的候选里（后端保存时也会剔除，这里提前保持一致）。
  const itemIDs = React.useMemo(
    () => profiles.filter((profile) => String(profile.id) !== headProfileId).map((profile) => String(profile.id)),
    [profiles, headProfileId],
  );
  const head = headProfileId ? profilesByID.get(headProfileId) : undefined;

  const label = React.useCallback(
    (id: string) => {
      const profile = profilesByID.get(id);
      return profile ? `${profile.name} ${profile.model}` : t("profileRef", { id });
    },
    [profilesByID, t],
  );

  const move = (index: number, offset: -1 | 1) => {
    const target = index + offset;
    if (target < 0 || target >= value.length) return;
    const next = [...value];
    [next[index], next[target]] = [next[target], next[index]];
    onValueChange(next);
  };

  return (
    <div className="grid gap-2">
      {!headProfileId && <p className="text-muted-foreground text-xs">{t("headRequired")}</p>}

      <Combobox
        items={itemIDs}
        itemToStringValue={label}
        multiple
        value={value}
        onValueChange={onValueChange}
        disabled={disabled}
      >
        <ComboboxChips>
          <ComboboxValue>
            {value.map((id) => (
              <ComboboxChip key={id}>{profilesByID.get(id)?.name ?? `#${id}`}</ComboboxChip>
            ))}
          </ComboboxValue>
          <ComboboxChipsInput
            id={inputId}
            placeholder={profiles.length > 0 ? t("searchPlaceholder") : t("noProfiles")}
            disabled={disabled ? true : profiles.length === 0}
          />
        </ComboboxChips>
        <ComboboxContent>
          <ComboboxEmpty>{t("noMatch")}</ComboboxEmpty>
          <ComboboxList>
            {(id) => {
              const profile = profilesByID.get(id);
              return (
                <ComboboxItem key={id} value={id} disabled={!profile}>
                  <div className="flex min-w-0 flex-1 flex-col">
                    <span className="flex min-w-0 items-center gap-2">
                      <span className="truncate">
                        {profile?.name ?? t("profileRef", { id })}
                        {profile?.is_default ? t("activeSuffix") : ""}
                      </span>
                    </span>
                    {profile && (
                      <span className="flex min-w-0 items-center gap-2">
                        <span className="truncate text-muted-foreground text-xs">{profile.model}</span>
                        <ProfileCapabilityBadges profile={profile} />
                      </span>
                    )}
                  </div>
                </ComboboxItem>
              );
            }}
          </ComboboxList>
        </ComboboxContent>
      </Combobox>

      {value.length === 0 ? (
        <p className="text-muted-foreground text-xs">{t("fallbackEmpty")}</p>
      ) : (
        <div className="flex flex-col divide-y rounded-lg border">
          {value.map((id, index) => {
            const profile = profilesByID.get(id);
            return (
              <div
                key={id}
                className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 px-2.5 py-2 sm:flex"
              >
                <span className="w-5 shrink-0 text-center text-muted-foreground text-xs tabular-nums">{index + 1}</span>
                <div className="min-w-0 sm:flex-1">
                  <p className="truncate font-medium text-sm">{profile?.name ?? t("profileRef", { id })}</p>
                  <div className="flex min-w-0 items-center gap-2">
                    <p className="truncate text-muted-foreground text-xs">{profile?.model ?? t("unavailable")}</p>
                    <ProfileCapabilityBadges profile={profile} />
                  </div>
                </div>
                <div className="col-span-2 col-start-2 flex shrink-0 items-center gap-1 justify-self-end sm:col-auto sm:justify-self-auto">
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t("moveUp")}
                    onClick={() => move(index, -1)}
                    disabled={disabled ? true : index === 0}
                  >
                    <ArrowUpIcon data-icon="inline-start" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t("moveDown")}
                    onClick={() => move(index, 1)}
                    disabled={disabled ? true : index === value.length - 1}
                  >
                    <ArrowDownIcon data-icon="inline-start" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t("remove")}
                    onClick={() => onValueChange(value.filter((x) => x !== id))}
                    disabled={disabled}
                  >
                    <XIcon data-icon="inline-start" />
                  </Button>
                </div>
              </div>
            );
          })}
        </div>
      )}

      <ChainCapabilityAdvice
        head={head}
        members={[head, ...value.map((id) => profilesByID.get(id))]}
      />
      <p className="text-muted-foreground text-xs">{t("roleScopeHint")}</p>
    </div>
  );
}
