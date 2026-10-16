"use client";

// ⌘K / Ctrl+K 命令面板：跳转（侧边栏一级导航）+ 操作（新建、主题模式与预设）+ 搜索（深层链接结果）。
// 「跳转」与「搜索」共用 sidebar-items.ts 单一数据源（含图标与路由，不另建硬编码列表）；
// 主题切换复用 preferences store 与 persistPreference，与偏好设置面板同链路。
// 快捷键主绑定 ⌘K / Ctrl+K，⌘J / Ctrl+J 保留为旧版别名（向后兼容）。
// 注：面板不读取侧边栏的收藏/隐藏偏好——与本次升级前的搜索行为一致。

import * as React from "react";

import { useRouter } from "next/navigation";

import { type LucideIcon, MessageSquare, Monitor, Moon, Search, Sun, Target } from "lucide-react";
import { useTranslations } from "next-intl";

import { Button } from "@/components/ui/button";
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { persistPreference } from "@/lib/preferences/preferences-storage";
import { THEME_MODE_OPTIONS, THEME_PRESET_OPTIONS, type ThemeMode, type ThemePreset } from "@/lib/preferences/theme";
import { applyThemePreset } from "@/lib/preferences/theme-utils";
import type { NavMainItem } from "@/navigation/sidebar/sidebar-items";
import { navGroupMessageKey, sidebarItems } from "@/navigation/sidebar/sidebar-items";
import { usePreferencesStore } from "@/stores/preferences/preferences-provider";

// 搜索数据仅保存 nav 命名空间键，不保存展示文本。
// 组件通过 useTranslations(nav) 在渲染时翻译，与侧边栏共用消息来源。
type SearchItem = {
  id: string;
  headingKey: string; // nav 相对组标题键，如 group.function。
  labelKey: string; // nav 相对条目键，如 item.dashboard。
  fallbackLabel: string; // 消息键不存在时使用的原始标签。
  url: string;
  icon?: NavMainItem["icon"];
  disabled?: boolean;
  newTab?: boolean;
  topLevel: boolean; // 一级导航条目 → 跳转组；子条目 → 搜索组的深层链接。
  keywords: string[]; // 条目 id + 口语化别名，供 cmdk 模糊匹配（不改展示文案）。
};

// 少量口语化检索别名：让「设置」「模型」等说法也能命中跳转项。
// 只补关键字，不维护第二份列表，展示文案仍来自 sidebar-items.ts。
const NAV_SEARCH_ALIASES: Record<string, string[]> = {
  chat: ["聊天", "会话"],
  llm: ["模型", "大模型"],
  mcp: ["服务器"],
  settings: ["设置", "偏好"],
  sync: ["数据源"],
  tasks: ["任务列表"],
};

const searchItems: SearchItem[] = sidebarItems.flatMap((group) => {
  const headingKey = navGroupMessageKey(group.id);
  return group.items.flatMap((item): SearchItem[] => {
    if (item.subItems) {
      return item.subItems.map((sub) => ({
        id: sub.id,
        headingKey,
        labelKey: `item.${sub.id}`,
        fallbackLabel: sub.title,
        url: sub.url,
        icon: item.icon,
        disabled: sub.disabled,
        newTab: sub.newTab,
        topLevel: false,
        keywords: [sub.id, ...(NAV_SEARCH_ALIASES[sub.id] ?? [])],
      }));
    }
    return [
      {
        id: item.id,
        headingKey,
        labelKey: `item.${item.id}`,
        fallbackLabel: item.title,
        url: item.url,
        icon: item.icon,
        disabled: item.disabled,
        newTab: item.newTab,
        topLevel: true,
        keywords: [item.id, ...(NAV_SEARCH_ALIASES[item.id] ?? [])],
      },
    ];
  });
});

function getAvailableItems(items: SearchItem[]) {
  return items.filter((item) => !item.disabled && !item.url.includes("coming-soon"));
}

// 跳转组与搜索组由同一份 searchItems 按层级拆分，避免同一入口出现两次：
// 一级条目进「跳转」，子条目（若未来出现）作为深层链接进「搜索」。
// 无查询时只显示可用项（旧搜索的默认视图）；输入查询后显示全量（含禁用项，保持旧搜索行为）。
const jumpItems = searchItems.filter((item) => item.topLevel);
const deepLinkItems = searchItems.filter((item) => !item.topLevel);
const availableJumpItems = getAvailableItems(jumpItems);
const availableDeepLinkItems = getAvailableItems(deepLinkItems);

// 「操作」条目：label 即最终展示文案；keywords 参与 cmdk 模糊匹配。
type PaletteAction = {
  id: string;
  label: string;
  icon?: LucideIcon;
  swatch?: string; // 主题预设用色点替代图标，与偏好设置里的预设下拉一致。
  keywords?: string[];
  checked?: boolean; // 当前生效的主题模式/预设，渲染勾选标记。
  run: () => void;
};

// 主题模式图标与切换器语义一致：浅色=太阳、深色=月亮、跟随系统=显示器。
const THEME_MODE_ICONS: Record<ThemeMode, LucideIcon> = {
  light: Sun,
  dark: Moon,
  system: Monitor,
};

export function SearchDialog() {
  const [open, setOpen] = React.useState(false);
  const [query, setQuery] = React.useState("");
  const router = useRouter();
  const t = useTranslations("search");
  const tNav = useTranslations("nav");
  const themeMode = usePreferencesStore((s) => s.themeMode);
  const resolvedThemeMode = usePreferencesStore((s) => s.resolvedThemeMode);
  const setThemeMode = usePreferencesStore((s) => s.setThemeMode);
  const themePreset = usePreferencesStore((s) => s.themePreset);
  const setThemePreset = usePreferencesStore((s) => s.setThemePreset);
  const labelOf = (item: SearchItem) => (tNav.has(item.labelKey) ? tNav(item.labelKey) : item.fallbackLabel);

  // 主绑定 ⌘K / Ctrl+K，别名 ⌘J / Ctrl+J（旧版快捷键，向后兼容）。
  // 与旧实现一致，输入框内也可唤起面板；额外挡掉 Shift/Alt 组合，
  // 避免与「⌘⇧J 打开开发者工具」等浏览器快捷键互相触发。
  React.useEffect(() => {
    const down = (e: KeyboardEvent) => {
      const key = e.key.toLowerCase();
      if ((key !== "k" && key !== "j") || !(e.metaKey || e.ctrlKey) || e.shiftKey || e.altKey) return;
      e.preventDefault();
      setOpen((prev) => !prev);
    };
    document.addEventListener("keydown", down);
    return () => document.removeEventListener("keydown", down);
  }, []);

  const handleOpenChange = (value: boolean) => {
    setOpen(value);
    if (!value) setQuery("");
  };

  const handleSelect = (item: SearchItem) => {
    if (item.disabled) return;
    handleOpenChange(false);
    if (item.newTab) {
      window.open(item.url, "_blank", "noopener,noreferrer");
    } else {
      router.push(item.url);
    }
  };

  // 执行动作后关闭面板（与旧搜索选中即关闭一致）。
  const handleAction = (action: PaletteAction) => {
    handleOpenChange(false);
    action.run();
  };

  // 主题切换复用偏好设置里的同一条链路：store + persistPreference
  // （theme_mode 对应的 DOM 应用由 PreferencesStoreProvider 监听 store 完成）。
  const selectThemeMode = (mode: ThemeMode) => {
    setThemeMode(mode);
    void persistPreference("theme_mode", mode);
  };

  const selectThemePreset = (preset: ThemePreset) => {
    applyThemePreset(preset);
    setThemePreset(preset);
    void persistPreference("theme_preset", preset);
  };

  const actions: PaletteAction[] = [
    {
      id: "new-task",
      label: t("action.newTask"),
      icon: Target,
      keywords: ["task", "create"],
      // 任务页的新建入口是页面内抽屉（CreateTaskSheet），不读 query 参数，
      // 无法从外部一步打开，因此这里跳转到任务列表页。
      run: () => router.push("/function/tasks"),
    },
    {
      id: "new-conversation",
      label: t("action.newConversation"),
      icon: MessageSquare,
      keywords: ["chat", "create"],
      // 对话页的「新建对话」是页内状态（selectConversation(null)）；不带 ?c= 打开 /chat
      // 即落在新建视图，无需额外触发。
      run: () => router.push("/chat"),
    },
    ...THEME_MODE_OPTIONS.map((mode) => ({
      id: `theme-mode-${mode.value}`,
      label: `主题：${mode.label}`,
      icon: THEME_MODE_ICONS[mode.value],
      keywords: ["主题", "模式", "theme", "mode", mode.value],
      checked: themeMode === mode.value,
      run: () => selectThemeMode(mode.value),
    })),
    // 7 个主题预设与偏好设置下拉共用 THEME_PRESET_OPTIONS；
    // LLM / 系统配置 / MCP 等设置直达不在这里重复，「跳转」组就是同一份侧边栏数据源。
    ...THEME_PRESET_OPTIONS.map((preset) => ({
      id: `theme-preset-${preset.value}`,
      label: `主题预设：${preset.label}`,
      swatch: resolvedThemeMode === "dark" ? preset.primary.dark : preset.primary.light,
      keywords: ["主题", "预设", "theme", "preset", preset.value],
      checked: themePreset === preset.value,
      run: () => selectThemePreset(preset.value),
    })),
  ];

  const shownJumpItems = query ? jumpItems : availableJumpItems;
  const shownDeepLinkItems = query ? deepLinkItems : availableDeepLinkItems;

  const renderNavItem = (item: SearchItem) => {
    const label = labelOf(item);
    return (
      <CommandItem
        disabled={item.disabled}
        key={item.id}
        value={`${tNav(item.headingKey)} ${label}`}
        keywords={item.keywords}
        onSelect={() => handleSelect(item)}
      >
        <span className="flex min-w-0 items-center gap-2">
          {item.icon && <item.icon />}
          <span className="truncate">{label}</span>
        </span>
      </CommandItem>
    );
  };

  return (
    <>
      <Button
        onClick={() => handleOpenChange(true)}
        variant="link"
        className="px-0! font-normal text-muted-foreground hover:no-underline"
      >
        <Search data-icon="inline-start" />
        {t("button")}
        <kbd className="inline-flex h-5 select-none items-center gap-1 rounded border bg-muted px-1.5 font-medium text-[10px]">
          <span className="text-xs">⌘</span>K
        </kbd>
      </Button>
      <CommandDialog open={open} onOpenChange={handleOpenChange}>
        <Command>
          <CommandInput autoFocus placeholder={t("placeholder")} value={query} onValueChange={setQuery} />
          <CommandList>
            <CommandEmpty>{t("empty")}</CommandEmpty>
            <CommandGroup heading={t("group.jump")}>{shownJumpItems.map(renderNavItem)}</CommandGroup>
            <CommandSeparator />
            <CommandGroup heading={t("group.actions")}>
              {actions.map((action) => (
                <CommandItem
                  data-checked={action.checked ? "true" : undefined}
                  key={action.id}
                  value={action.label}
                  keywords={action.keywords}
                  onSelect={() => handleAction(action)}
                >
                  <span className="flex min-w-0 items-center gap-2">
                    {action.icon && <action.icon />}
                    {action.swatch && (
                      <span className="size-2.5 rounded-full" style={{ backgroundColor: action.swatch }} />
                    )}
                    <span className="truncate">{action.label}</span>
                  </span>
                </CommandItem>
              ))}
            </CommandGroup>
            {shownDeepLinkItems.length > 0 && (
              <>
                <CommandSeparator />
                <CommandGroup heading={t("group.results")}>{shownDeepLinkItems.map(renderNavItem)}</CommandGroup>
              </>
            )}
          </CommandList>
        </Command>
      </CommandDialog>
    </>
  );
}
