"use client";

import Link from "next/link";

import { useShallow } from "zustand/react/shallow";

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
import { useCurrentUser } from "@/hooks/use-current-user";
import { sidebarItems } from "@/navigation/sidebar/sidebar-items";
import { usePreferencesStore } from "@/stores/preferences/preferences-provider";

import { NavMain } from "./nav-main";
import { NavUser } from "./nav-user";
import { SidebarCustomize } from "./sidebar-customize-dialog";

export function AppSidebar({ ...props }: React.ComponentProps<typeof Sidebar>) {
  const currentUser = useCurrentUser();
  const { sidebarVariant, sidebarCollapsible, isSynced } = usePreferencesStore(
    useShallow((s) => ({
      sidebarVariant: s.sidebarVariant,
      sidebarCollapsible: s.sidebarCollapsible,
      isSynced: s.isSynced,
    })),
  );

  const variant = isSynced ? sidebarVariant : props.variant;
  const collapsible = isSynced ? sidebarCollapsible : props.collapsible;

  return (
    <Sidebar {...props} variant={variant} collapsible={collapsible}>
      <SidebarHeader className="pb-1">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              asChild
              size="lg"
              className="group/brand relative overflow-hidden rounded-lg border border-sidebar-border/60 bg-gradient-to-br from-primary/10 via-sidebar-accent/40 to-transparent transition-all duration-200 hover:border-primary/40 hover:from-primary/15 data-[slot=sidebar-menu-button]:p-2!"
            >
              <Link prefetch={false} href="/function/tasks">
                <div className="relative flex size-8 shrink-0 items-center justify-center rounded-md bg-primary/15 text-primary ring-1 ring-primary/30 transition-transform duration-200 group-hover/brand:scale-105">
                  {/* eslint-disable-next-line @next/next/no-img-element */}
                  <img src="/logo.png" alt="ARTEX-REA" width={24} height={24} className="shrink-0" />
                  <span className="absolute -top-0.5 -right-0.5 size-2 rounded-full bg-emerald-500 ring-2 ring-sidebar" />
                </div>
                <div className="grid flex-1 text-left leading-tight">
                  <div className="flex items-center gap-1.5">
                    <span className="truncate font-display font-semibold text-sm tracking-tight">
                      {APP_CONFIG.name}
                    </span>
                    <span className="shrink-0 rounded bg-primary/12 px-1.5 py-0.5 font-mono text-[9px] font-semibold tracking-wider text-primary ring-1 ring-primary/25">
                      {"OPS // REA"}
                    </span>
                  </div>
                  <span className="truncate font-mono text-[10px] text-muted-foreground/85 tracking-tight">
                    AUTONOMOUS OFFENSIVE &amp; RE
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
      <SidebarFooter>
        <SidebarCustomize />
        <NavUser user={currentUser} />
      </SidebarFooter>
    </Sidebar>
  );
}
