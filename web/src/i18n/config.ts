// 本仓库只提供简体中文一种界面语言。
export const LOCALES = ["zh"] as const;
export type Locale = (typeof LOCALES)[number];
export const DEFAULT_LOCALE: Locale = "zh";

// 界面语言固定为简体中文，语言标识仅保留自身名称。
export const LOCALE_LABELS: Record<Locale, string> = {
  zh: "简体中文",
};

export const LOCALE_COOKIE = "artex_locale";
export const LOCALE_STORAGE_KEY = "artex_locale";

export function isLocale(raw: unknown): raw is Locale {
  return typeof raw === "string" && (LOCALES as readonly string[]).includes(raw);
}

// 旧浏览器存储和构建环境中的语言代码统一归一为简体中文。
export function normalizeLocale(raw: unknown): Locale | undefined {
  if (typeof raw !== "string") return undefined;
  const code = raw.trim().toLowerCase();
  return isLocale(code) ? code : DEFAULT_LOCALE;
}

// 静态导出不能依赖请求 cookie 或 headers。此值决定预渲染语言，
// 浏览器中的用户设置在水合后由 IntlRuntimeProvider 接管。
export function resolveLocale(): Locale {
  return normalizeLocale(process.env.NEXT_PUBLIC_LOCALE) ?? DEFAULT_LOCALE;
}
