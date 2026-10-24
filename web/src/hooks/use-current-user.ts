"use client";

import { useEffect, useState } from "react";

import { APP_CONFIG } from "@/config/app-config";
import { auth, type CurrentUser } from "@/lib/auth";

// 兜底身份也取产品名，避免"标题叫 ARTEX-HYDRA、角落写别的"这种两处硬编码漂移。
const FALLBACK: CurrentUser = {
  id: "1",
  name: APP_CONFIG.name,
  username: "artex-rea",
  email: "",
  avatar: "",
  role: "operator",
};

export function useCurrentUser(): CurrentUser {
  const [user, setUser] = useState<CurrentUser>(FALLBACK);

  useEffect(() => {
    const u = auth.getCurrentUser();
    if (u) setUser(u);
  }, []);

  return user;
}
