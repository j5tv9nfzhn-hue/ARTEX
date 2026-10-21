#!/usr/bin/env python3
"""runtime_harvest.py <config.json>

在**授权的** SPA 上驱动 Camoufox（Firefox 内核）采集运行时接口面，
沿用原 puppeteer 版的配置项与输出格式，只把执行引擎从 Chromium 换成 Camoufox。

三道具客户端门禁，与原脚本一一对应：
  1. 渲染门禁  -> 注入伪造的 cookie / localStorage
  2. 拦截器门禁 -> 把「未授权」的 code 字段改写成成功值
  3. 内容门禁  -> 用全量菜单/权限载荷打桩

config.json（除 baseUrl 外均可省略）：
{
  "baseUrl": "https://target/",
  "cookies":   [{"name":"auth","value":"b64json:{\\"id\\":1}"}],
  "localStorage": {"token":"x","isLogin":"1"},
  "neutralize": { "fields":["response_code","code","errno","ret"], "success":0,
                  "flags":{"success":true,"message":"ok"} },
  "forward": true,
  "loginUrlPattern": "/login",
  "stubs": [ {"match":"permission|menu|role","body":{ "...": "..." }} ],
  "routes": ["/dashboard","/device"],
  "apiPattern": "/api/|/rest/|/graphql",
  "proxy": "http://127.0.0.1:8080",
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "waitMs": 1200, "perRouteMs": 900, "headless": true,
  "captureResponses": true, "recordWs": true, "respMax": 600
}

输出（与旧版一致）：
  runtime_api.json   {shell, uniq, perRoute, rec, ws, wsEndpoints, sse}
  api_merged.txt     与 api_static.txt 合并后的去重路径

依赖：python -m pip install -U "camoufox[geoip]" && python -m camoufox fetch
"""

from __future__ import annotations

import base64
import json
import re
import sys
import time
from pathlib import Path
from urllib.parse import unquote, urlparse

DEFAULT_NEUTRALIZE = {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": {"success": True},
}
DEFAULT_API_PATTERN = r"/api/|/rest/|/service/|/graphql|/gateway/"


def load_cfg(path: str) -> dict:
    raw = Path(path).read_text(encoding="utf-8")
    cfg = json.loads(raw)
    if not cfg.get("baseUrl"):
        raise SystemExit("config.baseUrl 必填")
    if cfg.get("chromium"):
        print("[!] 配置里的 chromium 字段已忽略：本脚本只用 Camoufox，不再接受任意 Chromium 路径")

    cfg.setdefault("neutralize", DEFAULT_NEUTRALIZE)
    cfg.setdefault("forward", True)
    cfg.setdefault("apiPattern", DEFAULT_API_PATTERN)
    cfg.setdefault("loginUrlPattern", "/login")
    cfg.setdefault("routes", ["/"])
    cfg.setdefault("waitMs", 1200)
    cfg.setdefault("perRouteMs", 900)
    cfg.setdefault("headless", True)
    cfg.setdefault("waitUntil", "domcontentloaded")
    cfg.setdefault("routeTimeout", 12000)
    cfg.setdefault("captureResponses", True)
    cfg.setdefault("recordWs", True)
    cfg.setdefault("respMax", 600)
    cfg.setdefault("proxy", "")
    for stub in cfg.get("stubs") or []:
        stub["_re"] = re.compile(stub["match"], re.I)
    return cfg


def parse_proxy(raw: str) -> dict[str, str]:
    """Playwright 的 Firefox 不解析 server 里内联的凭据，必须拆成 username/password。"""
    raw = raw.strip()
    if "://" not in raw:
        raw = "http://" + raw
    u = urlparse(raw)
    host = u.hostname or ""
    if ":" in host:                      # IPv6 字面量
        host = f"[{host}]"
    out = {"server": f"{u.scheme}://{host}" + (f":{u.port}" if u.port else "")}
    if u.username:
        out["username"] = unquote(u.username)
    if u.password:
        out["password"] = unquote(u.password)
    return out


def resolve_timezone(proxy: dict[str, str] | None) -> str | None:
    """启动级 geoip 只配 locale/坐标/WebRTC，时区要自己解析后传给 context（camoufox#773）。"""
    try:
        from camoufox.geolocation import get_geolocation
        from camoufox.ip import Proxy, public_ip
    except Exception:
        return None
    try:
        if proxy:
            exit_ip = public_ip(Proxy(server=proxy["server"],
                                      username=proxy.get("username"),
                                      password=proxy.get("password")).as_string())
        else:
            exit_ip = public_ip()
        return get_geolocation(exit_ip).timezone
    except Exception:
        return None


def cookie_value(v: str) -> str:
    """与旧版一致的三种写法：b64json:<原始JSON> → base64；json:<原文> → 原文；否则原样。"""
    if isinstance(v, str) and v.startswith("b64json:"):
        return base64.b64encode(v[8:].encode("utf-8")).decode("ascii")
    if isinstance(v, str) and v.startswith("json:"):
        return v[5:]
    return v


def neutralize(text: str, rule: dict) -> str:
    try:
        data = json.loads(text)
    except Exception:
        return text
    if not isinstance(data, dict):
        return text
    for field in rule.get("fields") or []:
        if field in data:
            data[field] = rule.get("success", 0)
    data.update(rule.get("flags") or {})
    return json.dumps(data, ensure_ascii=False)


def main() -> int:
    if len(sys.argv) < 2:
        print("用法：runtime_harvest.py <config.json>", file=sys.stderr)
        return 2

    try:
        from camoufox.sync_api import Camoufox
    except Exception as exc:
        print('[!] 未安装 camoufox：python -m pip install -U "camoufox[geoip]" && python -m camoufox fetch', file=sys.stderr)
        print(f"    原始错误：{exc}", file=sys.stderr)
        return 2

    cfg_path = Path(sys.argv[1]).resolve()
    cfg = load_cfg(str(cfg_path))
    outdir = cfg_path.parent

    base = urlparse(cfg["baseUrl"])
    origin = f"{base.scheme}://{base.netloc}"
    host = base.hostname or ""

    rec: list[dict] = []
    chunks: set[str] = set()
    ws_frames: list[dict] = []
    ws_endpoints: set[str] = set()
    sse: set[str] = set()

    launch: dict = {"headless": bool(cfg["headless"])}
    proxy = parse_proxy(cfg["proxy"]) if cfg.get("proxy") else None
    tz = None
    if proxy:
        launch["proxy"] = proxy
        launch["geoip"] = True  # 有代理时让 locale/坐标/WebRTC 跟随出口 IP
        tz = resolve_timezone(proxy)

    with Camoufox(**launch) as browser:
        # 时区必须在 context 创建时给：geoip 的启动级 API 不会应用到页面（camoufox#773）。
        ctx = None
        if tz:
            try:
                ctx = browser.new_context(timezone_id=tz)
            except Exception:
                ctx = None
        if ctx is None:
            ctx = browser.contexts[0] if getattr(browser, "contexts", None) else browser
        page = ctx.new_page()

        # —— 记录 WebSocket 帧（不依赖 CDP，Playwright 原生事件） ——
        if cfg["recordWs"]:
            def on_ws(sock) -> None:
                try:
                    ws_endpoints.add(sock.url)
                except Exception:
                    pass

                def keep(direction):
                    def handler(payload):
                        data = payload.decode("utf-8", "replace") if isinstance(payload, bytes) else str(payload)
                        ws_frames.append(
                            {"url": sock.url.replace(origin, ""), "dir": direction, "data": data[: cfg["respMax"]]}
                        )
                    return handler

                sock.on("framesent", keep("send"))
                sock.on("framereceived", keep("recv"))

            page.on("websocket", on_ws)

        # —— 渲染门禁：每次文档加载前注入伪造登录态 ——
        if cfg.get("localStorage"):
            kv = json.dumps(cfg["localStorage"], ensure_ascii=False)
            page.add_init_script(script=(
                "(() => { const kv = %s; try { for (const k in kv) localStorage.setItem(k, kv[k]); } catch (e) {} })();"
                % kv
            ))
        # —— 兜底：拦住跳转到登录页的整页导航 ——
        pattern = json.dumps(cfg["loginUrlPattern"])
        page.add_init_script(script="""
(() => {
  const pat = %s;
  const bad = u => { try { return String(u).indexOf(pat) >= 0; } catch (e) { return false; } };
  try {
    const d = Object.getOwnPropertyDescriptor(Location.prototype, 'href');
    Object.defineProperty(Location.prototype, 'href', { configurable: true,
      get() { return d.get.call(this); },
      set(v) { if (bad(v)) return; return d.set.call(this, v); } });
    const a = Location.prototype.assign, r = Location.prototype.replace;
    Location.prototype.assign = function (v) { if (bad(v)) return; return a.call(this, v); };
    Location.prototype.replace = function (v) { if (bad(v)) return; return r.call(this, v); };
  } catch (e) {}
})();
""" % pattern)

        # —— 设置登录 cookie ——
        if cfg.get("cookies"):
            cookies = []
            for c in cfg["cookies"]:
                cookies.append({
                    "name": c["name"],
                    "value": cookie_value(c.get("value", "")),
                    "domain": host,
                    "path": c.get("path", "/"),
                })
            ctx.add_cookies(cookies)

        # —— 请求拦截：记录 / 打桩 / 改写鉴权码 ——
        api_re = re.compile(cfg["apiPattern"])

        def handle(route, request) -> None:
            url = request.url
            method = request.method

            if re.search(r"\.js(\?|$)", url) and re.search(r"/assets/|/static/|/js/", url):
                chunks.add(url.rsplit("/", 1)[-1])

            # 兜底：应用启动后仍想跳登录页的导航，直接吞掉
            try:
                is_main_nav = request.is_navigation_request() and request.frame == page.main_frame
            except Exception:
                is_main_nav = False
            if is_main_nav and cfg["loginUrlPattern"] in url and len(rec) > 3:
                route.fulfill(status=204, body="")
                return

            if not api_re.search(url):
                route.continue_()
                return

            body = request.post_data
            entry = {
                "m": method,
                "u": url.replace(origin, "").split("?")[0],
                "full": url,
                "b": body[:400] if body else None,
            }
            rec.append(entry)

            stub = next((s for s in cfg.get("stubs") or [] if s["_re"].search(url)), None)
            if stub is not None:
                route.fulfill(status=200, content_type="application/json",
                              body=json.dumps(stub["body"], ensure_ascii=False))
                return

            accept = (request.headers or {}).get("accept", "")
            if "text/event-stream" in accept:
                sse.add(entry["u"])
                route.fulfill(status=200, content_type="application/json", body="{}")
                return

            if not cfg["forward"]:
                route.fulfill(status=200, content_type="application/json",
                              body=neutralize('{"data":{},"list":[],"total":0}', cfg["neutralize"]))
                return

            try:
                resp = route.fetch()
                text = resp.text()
                if cfg["captureResponses"]:
                    entry["resp"] = text[: cfg["respMax"]]
                    entry["ct"] = (resp.headers or {}).get("content-type", "")
                route.fulfill(status=200, content_type="application/json",
                              body=neutralize(text, cfg["neutralize"]))
            except Exception as exc:  # 单条转发失败不应中断整轮采集
                print(f"[!] 转发失败 {method} {entry['u']}: {exc}")
                route.fulfill(status=200, content_type="application/json",
                              body='{"response_code":0,"code":0,"data":{}}')

        ctx.route("**/*", handle)

        # —— 启动 ——
        try:
            page.goto(cfg["baseUrl"], wait_until=cfg["waitUntil"], timeout=45000)
        except Exception as exc:
            print(f"[goto] {exc}")
        page.wait_for_timeout(cfg["waitMs"])

        shell = page.evaluate("""() => ({
            url: location.href,
            loginForm: !!document.querySelector('input[type=password]'),
            links: [...document.querySelectorAll('a[href^="/"]')].map(a => a.getAttribute('href'))
        })""")
        print(f"[*] 引导后：url={shell['url']}  登录表单={shell['loginForm']}")
        if shell["loginForm"]:
            print("[!] 仍然停在登录页——回查 config 里的 cookie / localStorage 是否与目标渲染门禁匹配")

        routes = list(dict.fromkeys(list(cfg["routes"]) + [h for h in shell["links"] if h and len(h) > 1]))
        per_route: dict[str, list[str]] = {}
        for rt in routes:
            before = len(rec)
            try:
                page.goto(origin + rt, wait_until=cfg["waitUntil"], timeout=cfg["routeTimeout"])
            except Exception:
                pass
            page.wait_for_timeout(cfg["perRouteMs"])
            per_route[rt] = sorted({f"{r['m']} {r['u']}" for r in rec[before:]})

    uniq = sorted({f"{r['m']} {r['u']}" for r in rec})
    ws_uniq = sorted({u.replace(origin, "") for u in ws_endpoints if u})

    (outdir / "runtime_api.json").write_text(
        json.dumps(
            {
                "shell": shell,
                "uniq": uniq,
                "perRoute": per_route,
                "rec": rec,
                "ws": ws_frames,
                "wsEndpoints": ws_uniq,
                "sse": sorted(sse),
            },
            ensure_ascii=False,
            indent=2,
        ),
        encoding="utf-8",
    )

    merged = {x.split(" ", 1)[1] for x in uniq}
    static_file = outdir / "api_static.txt"
    if static_file.exists():
        for line in static_file.read_text(encoding="utf-8").splitlines():
            if line.strip():
                merged.add(line.strip())
    (outdir / "api_merged.txt").write_text("\n".join(sorted(merged)) + "\n", encoding="utf-8")

    print(f"[+] 运行时接口（带方法）：{len(uniq)}")
    print(f"[+] 运行时看到的 JS chunk：{len(chunks)}")
    if cfg["recordWs"]:
        print(f"[+] WebSocket 端点：{len(ws_uniq)}（{len(ws_frames)} 帧）| SSE 端点：{len(sse)}")
    if cfg["captureResponses"]:
        print(f"[+] 已捕获响应体：{sum(1 for r in rec if 'resp' in r)}/{len(rec)}")
    print(f"[+] 合并去重路径：{len(merged)} -> api_merged.txt")
    print("[+] 完整明细 -> runtime_api.json（按路由 + 响应体 + WS 帧 + SSE）")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
