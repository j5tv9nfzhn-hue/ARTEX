"use client";

import { useTranslations } from "next-intl";

import { type StatusDomain, statusMeta, type Tone, toneClasses, toneDot } from "@/lib/status";
import { cn } from "@/lib/utils";

export function StatusBadge({
  domain,
  value,
  dot = false,
  className,
}: {
  domain: StatusDomain;
  value: string;
  dot?: boolean;
  className?: string;
}) {
  const meta = statusMeta(domain, value);
  // Shared status labels live in the "status" message namespace (ko/zh);
  // lib/status.ts only carries the color tone. The raw value is the last-resort
  // fallback for an unknown status (every real status.* key exists in ko/zh).
  const t = useTranslations("status");
  const key = `${domain}.${value}`;
  const label = t.has(key) ? t(key) : value;
  // severity 域由 lib/status.ts 直接携带完整 class(badgeClass/dotClass,源自
  // lib/severity.ts 唯一事实源);其余域按 tone 推导。
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium whitespace-nowrap",
        meta.badgeClass ?? toneClasses[meta.tone],
        className,
      )}
    >
      {dot && <span className={cn("size-1.5 rounded-full", meta.dotClass ?? toneDot[meta.tone])} />}
      {label}
    </span>
  );
}

export function ToneDot({ tone }: { tone: Tone }) {
  return <span className={cn("size-2 rounded-full", toneDot[tone])} />;
}
