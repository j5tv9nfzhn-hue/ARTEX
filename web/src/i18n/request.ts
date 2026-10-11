import { getRequestConfig } from "next-intl/server";

import { type Locale, resolveLocale } from "./config";

// 显式 locale 消息导入映射，避免打包整个 messages 目录。
const loaders: Record<Locale, () => Promise<{ default: Record<string, unknown> }>> = {
  zh: () => import("../../messages/zh.json"),
};

// next-intl 请求配置直接决定预渲染语言，不使用语言路径或中间件。
// 不读取 requestLocale，不触发动态渲染；运行时由 IntlRuntimeProvider 切换。
export default getRequestConfig(async () => {
  const locale = resolveLocale();
  const messages = (await loaders[locale]()).default;
  return { locale, messages };
});
