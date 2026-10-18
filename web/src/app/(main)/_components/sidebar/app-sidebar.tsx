"use client";

import Link from "next/link";

import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar";
import { APP_CONFIG } from "@/config/app-config";
import { sidebarItems } from "@/navigation/sidebar/sidebar-items";
import { usePreferencesStore } from "@/stores/preferences/preferences-provider";

import { NavMain } from "./nav-main";
import { NavUser } from "./nav-user";

export function AppSidebar({ ...props }: React.ComponentProps<typeof Sidebar>) {
  const sidebarVariant = usePreferencesStore((s) => s.sidebarVariant);
  const sidebarCollapsible = usePreferencesStore((s) => s.sidebarCollapsible);

  return (
    <Sidebar {...props} variant={sidebarVariant} collapsible={sidebarCollapsible}>
      <SidebarHeader className="border-b border-sidebar-border/60 pb-2.5">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton asChild className="h-10 gap-2.5 px-2 hover:bg-sidebar-accent/70">
              <Link prefetch={false} href="/dashboard" aria-label={APP_CONFIG.name}>
                <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-linear-to-br from-primary/20 to-chart-2/15 shadow-2xs ring-1 ring-primary/35">
                  {/* eslint-disable-next-line @next/next/no-img-element */}
                  <img src="/logo.png" alt="ARTEX-REA" width={20} height={20} className="size-5 shrink-0 dark:invert" />
                </span>
                <div className="flex min-w-0 flex-1 items-center justify-between gap-1.5 group-data-[collapsible=icon]:hidden">
                  <span className="truncate font-display text-base font-bold tracking-tight">
                    <span>ARTEX</span>
                    <span className="bg-linear-to-r from-primary to-chart-2 bg-clip-text text-transparent">-REA</span>
                  </span>
                  <span className="shrink-0 rounded bg-primary/12 px-1.5 py-0.5 font-mono text-[9px] font-semibold tracking-wider text-primary ring-1 ring-primary/25">
                    OPS // REA
                  </span>
                </div>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        <NavMain items={sidebarItems} />
      </SidebarContent>
      <SidebarFooter className="border-t border-sidebar-border/60 pt-2">
        <NavUser />
      </SidebarFooter>
    </Sidebar>
  );
}
