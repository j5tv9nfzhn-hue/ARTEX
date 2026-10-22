import * as React from "react";

// GlobePhone 是侧边栏「局域网连接」那一项的自定义图标：
// 一个地球，右下角叠一部手机——意思是「这台机器和手机在同一个局域网里」。
//
// 造型贴着 lucide 的 24×24、2px 圆头描边，混在旁边的内置图标里看不出接缝。
// 侧边栏拿它当 LucideIcon 用（见 navigation/sidebar/sidebar-items.ts），
// 那边会补一次类型断言：普通函数组件和 forwardRef 组件的类型对不上，但渲染行为一致。

type GlobePhoneProps = React.ComponentProps<"svg"> & {
  size?: number | string;
  strokeWidth?: number | string;
};

export function GlobePhone({ size = 24, strokeWidth = 2, ...props }: GlobePhoneProps) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      {...props}
    >
      {/* 地球：外圈 + 赤道 + 两条经线 */}
      <circle cx="9" cy="9" r="6.6" />
      <path d="M2.6 7.4h12.8" />
      <path d="M2.6 11.2h8.2" />
      <path d="M9 2.4c-1.8 2-2.8 4.6-2.8 7 0 1.4.3 2.7.8 3.9" />
      {/* 手机：叠在地球右下角 */}
      <rect x="13.6" y="11.4" width="6.8" height="10" rx="1.8" />
      <path d="M16.4 19h1.2" />
    </svg>
  );
}
