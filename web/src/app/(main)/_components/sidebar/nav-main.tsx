"use client";

import * as React from "react";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { ChevronRight, MailIcon, PlusCircleIcon } from "lucide-react";
import { useTranslations } from "next-intl";

import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  useSidebar,
} from "@/components/ui/sidebar";
import { api } from "@/lib/api";
import { toggleSidebarGroupCollapsed, useSidebarPrefs } from "@/lib/sidebar-prefs";
import { cn } from "@/lib/utils";
import type {
  NavBadge,
  NavGroup,
  NavMainItem,
  NavMainLinkItem,
  NavMainParentItem,
} from "@/navigation/sidebar/sidebar-items";
import { navGroupMessageKey } from "@/navigation/sidebar/sidebar-items";

interface NavMainProps {
  readonly items: readonly NavGroup[];
}
interface NavItemProps {
  readonly item: NavMainItem;
  readonly isItemActive: (item: NavMainItem) => boolean;
  readonly isSubItemActive: (url: string) => boolean;
  readonly isSubmenuOpen: (item: NavMainParentItem) => boolean;
}

interface NavLinkItemProps {
  readonly item: NavMainLinkItem;
  readonly isActive: boolean;
  readonly showIconFallback: boolean;
}

interface NavLinkIconProps {
  readonly item: NavMainLinkItem;
  readonly title: string;
  readonly showFallback: boolean;
}

interface NavDropdownItemProps {
  readonly item: NavMainParentItem;
  readonly isActive: boolean;
  readonly isSubItemActive: (url: string) => boolean;
}

interface NavCollapsibleItemProps {
  readonly item: NavMainParentItem;
  readonly isActive: boolean;
  readonly defaultOpen: boolean;
  readonly isSubItemActive: (url: string) => boolean;
}

interface NavGroupSectionProps {
  readonly label: string;
  readonly items: readonly NavMainItem[];
  readonly collapsible: boolean;
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  readonly isItemActive: (item: NavMainItem) => boolean;
  readonly isSubItemActive: (url: string) => boolean;
  readonly isSubmenuOpen: (item: NavMainParentItem) => boolean;
}

function CollapsedIconFallback({ title }: { title: string }) {
  return (
    <span className="flex size-4 shrink-0 items-center justify-center rounded-xs font-medium text-[10px] outline">
      {title.slice(0, 1)}
    </span>
  );
}

function hasSubItems(item: NavMainItem): item is NavMainParentItem {
  return Boolean(item.subItems?.length);
}

// 收藏分组使用保留 id 0，用于折叠状态的持久化键。
const PINNED_GROUP_ID = 0;

export function NavMain({ items }: NavMainProps) {
  const t = useTranslations("nav");
  const rawPath = usePathname();
  const { state, isMobile } = useSidebar();
  const prefs = useSidebarPrefs();
  // 路由段含中文，pathname 可能是百分号编码，解码后再与导航 url 比较，保证高亮命中。
  const path = (() => {
    try {
      return decodeURIComponent(rawPath);
    } catch {
      return rawPath;
    }
  })();

  const isItemActive = (item: NavMainItem) => {
    if (hasSubItems(item)) {
      return item.subItems.some((sub) => path.startsWith(sub.url));
    }

    return path === item.url;
  };

  const isSubItemActive = (url: string) => {
    return path === url;
  };

  const isSubmenuOpen = (item: NavMainParentItem) => {
    return item.subItems.some((sub) => path.startsWith(sub.url));
  };

  // 图标折叠模式下分组标签被 CSS 隐藏（opacity-0/-mt-8），若再折叠内容会完全不可达；
  // 因此该模式始终展开分组（保持改造前行为），只有非图标模式允许折叠。
  const groupsCollapsible = !(state === "collapsed" && !isMobile);

  const hiddenSet = new Set(prefs.hidden);
  const pinnedSet = new Set(prefs.pinned);

  // 收藏区按置顶顺序跨分组取条目；置顶条目同时从原分组移除（pinnedSet 过滤），
  // 避免同一入口在侧边栏出现两次。
  const itemById = new Map<string, NavMainItem>();
  for (const group of items) {
    for (const item of group.items) itemById.set(item.id, item);
  }
  const pinnedItems = prefs.pinned
    .map((id) => itemById.get(id))
    .filter((item): item is NavMainItem => item !== undefined && !hiddenSet.has(item.id));

  // 隐藏的条目不再参与任何分组；分组因此清空时整组隐藏（含分组标签）。
  const visibleGroups = items
    .map((group) => ({
      ...group,
      items: group.items.filter((item) => !hiddenSet.has(item.id) && !pinnedSet.has(item.id)),
    }))
    .filter((group) => group.items.length > 0);

  const groupLabel = (group: NavGroup) => {
    const key = navGroupMessageKey(group.id);
    return t.has(key) ? t(key) : (group.label ?? "");
  };

  return (
    <>
      {pinnedItems.length > 0 && (
        <NavGroupSection
          label={t("group.pinned")}
          items={pinnedItems}
          collapsible={groupsCollapsible}
          open={!prefs.collapsedGroups.includes(PINNED_GROUP_ID)}
          onOpenChange={(open) => toggleSidebarGroupCollapsed(PINNED_GROUP_ID, !open)}
          isItemActive={isItemActive}
          isSubItemActive={isSubItemActive}
          isSubmenuOpen={isSubmenuOpen}
        />
      )}
      {visibleGroups.map((group) => (
        <NavGroupSection
          key={group.id}
          label={groupLabel(group)}
          items={group.items}
          collapsible={groupsCollapsible && groupLabel(group) !== ""}
          open={!prefs.collapsedGroups.includes(group.id)}
          onOpenChange={(open) => toggleSidebarGroupCollapsed(group.id, !open)}
          isItemActive={isItemActive}
          isSubItemActive={isSubItemActive}
          isSubmenuOpen={isSubmenuOpen}
        />
      ))}
    </>
  );
}

// NavGroupSection renders one sidebar group. When collapsible, the group label is a
// trigger and the open state is controlled by the caller so it can be persisted
// (sidebar-prefs); when not, the label is static like the original rendering.
function NavGroupSection({
  label,
  items,
  collapsible,
  open,
  onOpenChange,
  isItemActive,
  isSubItemActive,
  isSubmenuOpen,
}: NavGroupSectionProps) {
  const content = (
    <SidebarGroupContent>
      <SidebarMenu>
        {items.map((item) => (
          <NavItem
            key={item.id}
            item={item}
            isItemActive={isItemActive}
            isSubItemActive={isSubItemActive}
            isSubmenuOpen={isSubmenuOpen}
          />
        ))}
      </SidebarMenu>
    </SidebarGroupContent>
  );

  if (!collapsible) {
    return (
      <SidebarGroup>
        {label !== "" && (
          <SidebarGroupLabel className="group-data-[collapsible=icon]:pointer-events-none">{label}</SidebarGroupLabel>
        )}
        {content}
      </SidebarGroup>
    );
  }

  return (
    <Collapsible open={open} onOpenChange={onOpenChange} asChild className="group/collapsible">
      <SidebarGroup>
        <SidebarGroupLabel asChild className="cursor-pointer select-none hover:bg-sidebar-accent">
          <CollapsibleTrigger>
            {label}
            <ChevronRight className="ml-auto transition-transform duration-200 group-data-[state=open]/collapsible:rotate-90" />
          </CollapsibleTrigger>
        </SidebarGroupLabel>
        <CollapsibleContent>{content}</CollapsibleContent>
      </SidebarGroup>
    </Collapsible>
  );
}

function NavItem({ item, isItemActive, isSubItemActive, isSubmenuOpen }: NavItemProps) {
  const { state, isMobile } = useSidebar();
  const isCollapsedDesktop = state === "collapsed" && !isMobile;

  if (!hasSubItems(item)) {
    return <NavLinkItem item={item} isActive={isItemActive(item)} showIconFallback={isCollapsedDesktop} />;
  }

  if (isCollapsedDesktop) {
    return <NavDropdownItem item={item} isActive={isItemActive(item)} isSubItemActive={isSubItemActive} />;
  }

  return (
    <NavCollapsibleItem
      item={item}
      isActive={isItemActive(item)}
      defaultOpen={isSubmenuOpen(item)}
      isSubItemActive={isSubItemActive}
    />
  );
}

// InterceptPendingBadge polls /api/intercept/pending every 5 s and shows a red
// count badge when there are unresolved approval requests.
function InterceptPendingBadge() {
  const [count, setCount] = React.useState(0);
  React.useEffect(() => {
    let live = true;
    async function poll() {
      try {
        const list = await api.interceptPending();
        if (live) setCount(list.length);
      } catch {
        /* ignore */
      }
    }
    void poll();
    const t = setInterval(poll, 5000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, []);
  if (count === 0) return null;
  return (
    <SidebarMenuBadge className="flex h-4.5 min-w-[18px] items-center justify-center rounded-full bg-amber-500 px-1 text-[10px] font-semibold leading-none text-white shadow-sm ring-1 ring-amber-400/40">
      {count > 99 ? "99+" : count}
    </SidebarMenuBadge>
  );
}

function NavLinkItem({ item, isActive, showIconFallback }: NavLinkItemProps) {
  const t = useTranslations("nav");
  const title = t(`item.${item.id}`);
  return (
    <SidebarMenuItem>
      <SidebarMenuButton asChild aria-disabled={item.disabled} tooltip={title} isActive={isActive}>
        <Link
          prefetch={false}
          href={item.url}
          target={item.newTab ? "_blank" : undefined}
          rel={item.newTab ? "noreferrer" : undefined}
        >
          <NavLinkIcon item={item} title={title} showFallback={showIconFallback} />
          <span>{title}</span>
        </Link>
      </SidebarMenuButton>
      {item.id === "approvals" ? <InterceptPendingBadge /> : <NavItemBadge badge={item.badge} />}
    </SidebarMenuItem>
  );
}

function NavLinkIcon({ item, title, showFallback }: NavLinkIconProps) {
  const Icon = item.icon;

  if (Icon) {
    return <Icon />;
  }

  if (showFallback) {
    return <CollapsedIconFallback title={title} />;
  }

  return null;
}

function NavDropdownItem({ item, isActive, isSubItemActive }: NavDropdownItemProps) {
  const t = useTranslations("nav");
  const Icon = item.icon;
  const title = t(`item.${item.id}`);
  const subTitle = (id: string, fallback: string) => (t.has(`item.${id}`) ? t(`item.${id}`) : fallback);

  return (
    <SidebarMenuItem>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <SidebarMenuButton tooltip={title} isActive={isActive} disabled={item.disabled}>
            {Icon ? <Icon /> : <CollapsedIconFallback title={title} />}
            <span>{title}</span>
          </SidebarMenuButton>
        </DropdownMenuTrigger>

        <DropdownMenuContent side="right" align="start" sideOffset={12} className="w-48">
          <DropdownMenuGroup>
            {item.subItems.map((subItem) => {
              const SubIcon = subItem.icon;

              return (
                <DropdownMenuItem key={subItem.id} asChild disabled={subItem.disabled}>
                  <Link
                    prefetch={false}
                    href={subItem.url}
                    target={subItem.newTab ? "_blank" : undefined}
                    rel={subItem.newTab ? "noreferrer" : undefined}
                    aria-current={isSubItemActive(subItem.url) ? "page" : undefined}
                    className="flex items-center gap-2"
                  >
                    {SubIcon && <SubIcon />}
                    <span>{subTitle(subItem.id, subItem.title)}</span>
                  </Link>
                </DropdownMenuItem>
              );
            })}
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
    </SidebarMenuItem>
  );
}

function NavCollapsibleItem({ item, isActive, defaultOpen, isSubItemActive }: NavCollapsibleItemProps) {
  const t = useTranslations("nav");
  const Icon = item.icon;
  const title = t(`item.${item.id}`);
  const subTitle = (id: string, fallback: string) => (t.has(`item.${id}`) ? t(`item.${id}`) : fallback);

  return (
    <Collapsible asChild defaultOpen={defaultOpen} className="group/collapsible">
      <SidebarMenuItem>
        <CollapsibleTrigger asChild>
          <SidebarMenuButton tooltip={title} isActive={isActive} disabled={item.disabled}>
            {Icon && <Icon />}
            <span>{title}</span>
            <ChevronRight className="ml-auto transition-transform duration-200 group-data-[state=open]/collapsible:rotate-90" />
          </SidebarMenuButton>
        </CollapsibleTrigger>
        <NavItemBadge badge={item.badge} />

        <CollapsibleContent>
          <SidebarMenuSub>
            {item.subItems.map((subItem) => {
              const SubIcon = subItem.icon;

              return (
                <SidebarMenuSubItem key={subItem.id}>
                  <SidebarMenuSubButton
                    asChild
                    aria-disabled={subItem.disabled}
                    isActive={isSubItemActive(subItem.url)}
                  >
                    <Link
                      prefetch={false}
                      href={subItem.url}
                      target={subItem.newTab ? "_blank" : undefined}
                      rel={subItem.newTab ? "noreferrer" : undefined}
                    >
                      {SubIcon && <SubIcon />}
                      <span>{subTitle(subItem.id, subItem.title)}</span>
                    </Link>
                  </SidebarMenuSubButton>
                </SidebarMenuSubItem>
              );
            })}
          </SidebarMenuSub>
        </CollapsibleContent>
      </SidebarMenuItem>
    </Collapsible>
  );
}

function NavItemBadge({ badge }: { badge?: NavBadge }) {
  if (!badge) {
    return null;
  }

  return (
    <SidebarMenuBadge
      className={cn(
        "rounded-sm border capitalize",
        badge === "new" &&
          "border-green-600 text-green-600 peer-hover/menu-button:text-green-600 peer-data-active/menu-button:text-green-600",
        badge === "soon" && "border-muted-foreground text-muted-foreground",
      )}
    >
      {badge}
    </SidebarMenuBadge>
  );
}
