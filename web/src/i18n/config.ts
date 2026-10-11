// 界面支持简体中文、英语和西班牙语；简体中文为默认语言。
export const LOCALES = ["zh", "en", "es"] as const;
export type Locale = (typeof LOCALES)[number];
export const DEFAULT_LOCALE: Locale = "zh";

// 语言选择器使用各语言自身的名称。
export const LOCALE_LABELS: Record<Locale, string> = {
  zh: "简体中文",
  en: "English",
  es: "Español",
};

export const LOCALE_COOKIE = "artex_locale";
export const LOCALE_STORAGE_KEY = "artex_locale";

export function isLocale(raw: unknown): raw is Locale {
  return typeof raw === "string" && (LOCALES as readonly string[]).includes(raw);
}

// 兼容旧浏览器存储和构建环境中的 ko；旧语言代码统一迁移为简体中文。
export function normalizeLocale(raw: unknown): Locale | undefined {
  if (typeof raw !== "string") return undefined;
  const code = raw.trim().toLowerCase();
  if (code === "ko") return "zh";
  return isLocale(code) ? code : undefined;
}

// 静态导出不能依赖请求 cookie 或 headers。此值决定预渲染语言，
// 浏览器中的用户设置在水合后由 IntlRuntimeProvider 接管。
export function resolveLocale(): Locale {
  return normalizeLocale(process.env.NEXT_PUBLIC_LOCALE) ?? DEFAULT_LOCALE;
}
