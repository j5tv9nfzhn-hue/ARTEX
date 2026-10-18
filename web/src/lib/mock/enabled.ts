import { DEV_BYPASS } from "@/lib/dev-mode";

// Mock 开关。构建期注入的公开变量（NEXT_PUBLIC_ 前缀才在浏览器可读）。
// Vercel 上设 NEXT_PUBLIC_MOCK=1 即整站走 mock、无需后端。
// 本机调试旁路（lib/dev-mode.ts）同样走 mock，因此调试阶段不需要后端在线。
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1" || DEV_BYPASS;
