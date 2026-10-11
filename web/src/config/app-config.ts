import { DEFAULT_LOCALE, type Locale, resolveLocale } from "@/i18n/config";

import packageJson from "../../package.json";

const currentYear = new Date().getFullYear();

// 浏览器标题与搜索引擎说明来自静态 metadata。本仓库只提供简体中文，
// 使用“产品名：说明”的标题格式。构建时确定的 metadata 不随运行时设置改变。
const META_BY_LOCALE: Record<Locale, { title: string; description: string }> = {
  zh: {
    title: "ARTEX：自主渗透测试控制台",
    description: "LLM 驱动的自主渗透测试系统控制台",
  },
};

export const APP_CONFIG = {
  name: "ARTEX",
  version: packageJson.version,
  copyright: `© ${currentYear}, ARTEX.`,
  meta: META_BY_LOCALE[resolveLocale()] ?? META_BY_LOCALE[DEFAULT_LOCALE],
};
