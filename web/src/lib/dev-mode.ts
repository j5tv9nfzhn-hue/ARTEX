/**
 * 本机调试旁路（只影响前端行为，不改变任何后端代码）。
 *
 * 目的：`next dev` 阶段不经后端、不要求登录，直接进面板看样式与布局改动。
 *
 * 取值：
 *   - `NEXT_PUBLIC_DEV_BYPASS=1` → 强制开启；
 *   - `NEXT_PUBLIC_DEV_BYPASS=0` → 强制关闭（dev 也走真实后端 + 登录）；
 *   - 未设置 → 开发构建默认开启，生产构建（`next build`）默认关闭。
 *
 * 开启后的三处效果：
 *   1. `src/proxy.ts` 放行全部页面，并把 /login、/setup 重定向到主界面；
 *   2. `lib/auth.ts` 视为已登录（假 token + 本地演示身份），客户端守卫不再跳登录；
 *   3. `lib/mock/enabled.ts` 的 MOCK 变为 true，`/api/*` 一律走 `lib/mock`，
 *      不发起任何真实网络请求，因此不需要后端在线。
 */
const FLAG = process.env.NEXT_PUBLIC_DEV_BYPASS;

export const DEV_BYPASS = FLAG === "0" ? false : FLAG === "1" || process.env.NODE_ENV !== "production";

/** 调试期用于放行守卫的假 token：不是 JWT，`getCurrentUser` 会单独兜底。 */
export const DEV_TOKEN = "dev-bypass";
