import { APP_CONFIG } from "@/config/app-config";
import { DEV_BYPASS, DEV_TOKEN } from "@/lib/dev-mode";

const TOKEN_KEY = "artex_token";
const COOKIE_MAX_AGE = 7 * 24 * 60 * 60; // 7 天（秒）

// Mock demo 与调试旁路都没有真实登录：给守卫一个假 token，界面直接进主面板。
const BYPASS_AUTH = process.env.NEXT_PUBLIC_MOCK === "1" || DEV_BYPASS;

export interface CurrentUser {
  id: string;
  name: string;
  username: string;
  email: string;
  avatar: string;
  role: string;
}

export const auth = {
  getToken(): string | null {
    if (typeof window === "undefined") return null;
    const stored = localStorage.getItem(TOKEN_KEY);
    if (stored) return stored;
    return BYPASS_AUTH ? DEV_TOKEN : null;
  },

  setToken(token: string): void {
    localStorage.setItem(TOKEN_KEY, token);
    // 同步写 cookie，供 Next.js middleware 服务端读取
    document.cookie = `${TOKEN_KEY}=${encodeURIComponent(token)}; path=/; max-age=${COOKIE_MAX_AGE}; SameSite=Lax`;
  },

  clearToken(): void {
    localStorage.removeItem(TOKEN_KEY);
    document.cookie = `${TOKEN_KEY}=; path=/; max-age=0`;
  },

  // 从 JWT payload 的 sub 字段解析当前用户，仅用于展示，不做签名验证。
  getCurrentUser(): CurrentUser | null {
    // 旁路 token 不是 JWT，按本地演示身份返回，避免右上角退化成兜底占位。
    if (BYPASS_AUTH) {
      return { id: "0", name: "本地调试", username: "dev-mode", email: "", avatar: "", role: "operator" };
    }

    const token = this.getToken();
    if (!token) return null;
    try {
      const parts = token.split(".");
      if (parts.length !== 3) return null;
      // base64url → base64
      const payload = JSON.parse(atob(parts[1].replace(/-/g, "+").replace(/_/g, "/")));
      // sub 是**登录凭据名**（历史值是 "ARTEX"，后端同时接受 ARTEX-HYDRA / ARTEX-REA / ARTEX），
      // 拿它当昵称会让左下角和左上角标题对不上。展示名统一取 APP_CONFIG.name
      // 这个唯一来源，凭据名单独留在 username 里——两者语义不同，不该混用。
      const username: string = payload.sub ?? "artex-rea";
      return { id: "1", name: APP_CONFIG.name, username, email: "", avatar: "", role: "operator" };
    } catch {
      return null;
    }
  },
};
