import type { ReactNode } from "react";

import { cookies } from "next/headers";

import { AppSidebar } from "@/app/(main)/_components/sidebar/app-sidebar";
import { AuthGuard } from "@/components/auth-guard";
import { FindingAlertMount } from "@/components/finding-alert";
import { NotificationBell } from "@/components/notification-bell";
import { SidebarInset, SidebarProvider, SidebarTrigger } from "@/components/ui/sidebar";
import { SIDEBAR_COLLAPSIBLE_VALUES, SIDEBAR_VARIANT_VALUES } from "@/lib/preferences/layout";
import { getPreference } from "@/server/server-actions";

import { MainContent } from "./_components/main-content";
import { LayoutControls } from "./_components/sidebar/layout-controls";
import { SearchDialog } from "./_components/sidebar/search-dialog";
import { ThemeSwitcher } from "./_components/sidebar/theme-switcher";

export default async function Layout({ children }: Readonly<{ children: ReactNode }>) {
  const cookieStore = await cookies();
  const defaultOpen = cookieStore.get("sidebar_state")?.value !== "false";
  const [variant, collapsible] = await Promise.all([
    getPreference("sidebar_variant", SIDEBAR_VARIANT_VALUES, "inset"),
    getPreference("sidebar_collapsible", SIDEBAR_COLLAPSIBLE_VALUES, "icon"),
  ]);

  return (
    <AuthGuard>
      {/* 全局新发现战报卡挂载点:渲染为 null,仅维护去重集合并在顶栏打开弹窗。 */}
      <FindingAlertMount />
      <SidebarProvider defaultOpen={defaultOpen}>
        <AppSidebar variant={variant} collapsible={collapsible} />
        <SidebarInset className="min-w-0">
          <header
            data-navbar-style="sticky"
            className="sticky top-0 z-50 flex h-12 shrink-0 items-center gap-2 border-b border-border/75 bg-background/85 backdrop-blur-md transition-[width,height] ease-linear after:pointer-events-none after:absolute after:inset-x-0 after:bottom-0 after:h-px after:bg-linear-to-r after:from-transparent after:via-primary/40 after:to-transparent group-has-data-[collapsible=icon]/sidebar-wrapper:h-12 md:rounded-t-xl"
          >
            <div className="flex w-full items-center justify-between px-4 lg:px-6">
              <div className="flex items-center gap-2 lg:gap-2.5">
                <SidebarTrigger className="-ml-1" />
                <div className="mx-0.5 hidden h-4 w-px bg-border/80 sm:block" />
                <SearchDialog />
              </div>
              <div className="flex items-center gap-1.5">
                <span className="mr-1 hidden items-center gap-1.5 rounded-full border border-primary/20 bg-primary/8 px-2.5 py-0.5 font-mono text-[10px] font-medium tracking-wider text-primary lg:inline-flex">
                  <span className="size-1.5 rounded-full bg-primary animate-pulse-soft" />
                  ARTEX-REA // COMMAND
                </span>
                <NotificationBell />
                <LayoutControls />
                <ThemeSwitcher />
              </div>
            </div>
          </header>
          <MainContent>{children}</MainContent>
        </SidebarInset>
      </SidebarProvider>
    </AuthGuard>
  );
}
