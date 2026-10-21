#!/usr/bin/env python3
"""camoufox-cli —— 会话式浏览器自动化命令行（Camoufox / Firefox 内核）。

为什么不是 playwright-cli：Playwright 的 Node CLI 只能驱动 Chromium 系浏览器（CDP），
而 Camoufox 是打了引擎级补丁的 Firefox（Juggler 协议），必须走 Playwright 的 Python
绑定。所以这里用「守护进程 + 短命令」的方式复刻原有用法：

    camoufox-cli open https://example.com
    camoufox-cli snapshot
    camoufox-cli click e3
    camoufox-cli close

守护进程常驻一个 Camoufox 实例，每条命令通过本机回环端口与它通讯，
因此页面状态、登录态、标签页在多次调用之间保持。

依赖：
    python -m pip install -U "camoufox[geoip]"
    python -m camoufox fetch

安全边界：只监听 127.0.0.1，随机端口 + 随机令牌；状态文件权限 0600。
"""

from __future__ import annotations

import json
import os
import secrets
import shutil
import socket
import subprocess
import sys
import time
from pathlib import Path
from typing import Any
from urllib.parse import unquote, urlparse

APP_DIR_NAME = "artex-camoufox"
IDLE_TIMEOUT = 45 * 60          # 守护进程空闲多久自动退出（秒）
CLIENT_TIMEOUT = 300            # 单条命令最长等待（秒），导航/截图可能较慢
START_TIMEOUT = 60              # 等待守护进程就绪（秒）

REF_ATTR = "data-cfx-ref"
REF_SELECTOR = f"[{REF_ATTR}]"


# --------------------------------------------------------------------------- #
# 运行时目录
# --------------------------------------------------------------------------- #

def runtime_dir() -> Path:
    if os.name == "nt":
        base = os.environ.get("LOCALAPPDATA") or os.path.expanduser("~")
    else:
        base = os.environ.get("XDG_STATE_HOME") or os.path.join(os.path.expanduser("~"), ".local", "state")
    d = Path(base) / APP_DIR_NAME
    d.mkdir(parents=True, exist_ok=True)
    return d


def state_file() -> Path:
    return runtime_dir() / "daemon.json"


def profile_dir() -> Path:
    return runtime_dir() / "profile"


def default_output_dir() -> Path:
    d = runtime_dir() / "out"
    d.mkdir(parents=True, exist_ok=True)
    return d


# --------------------------------------------------------------------------- #
# 工具函数
# --------------------------------------------------------------------------- #

def die(msg: str, code: int = 2) -> "None":
    print(msg, file=sys.stderr)
    raise SystemExit(code)


def load_state() -> dict[str, Any] | None:
    p = state_file()
    if not p.exists():
        return None
    try:
        return json.loads(p.read_text(encoding="utf-8"))
    except Exception:
        return None


def daemon_alive(st: dict[str, Any] | None) -> bool:
    if not st:
        return False
    try:
        with socket.create_connection(("127.0.0.1", int(st["port"])), timeout=1.0):
            return True
    except Exception:
        return False


def request(payload: dict[str, Any]) -> dict[str, Any]:
    st = load_state()
    if not daemon_alive(st):
        return {"ok": False, "error": "浏览器会话未启动：先执行 open"}
    payload = dict(payload)
    payload["token"] = st.get("token", "")
    s = socket.create_connection(("127.0.0.1", int(st["port"])), timeout=CLIENT_TIMEOUT)
    try:
        s.sendall((json.dumps(payload, ensure_ascii=False) + "\n").encode("utf-8"))
        buf = b""
        while not buf.endswith(b"\n"):
            chunk = s.recv(65536)
            if not chunk:
                break
            buf += chunk
    finally:
        s.close()
    if not buf.strip():
        return {"ok": False, "error": "守护进程没有返回结果（可能已崩溃）"}
    try:
        return json.loads(buf.decode("utf-8", "replace"))
    except Exception as exc:  # pragma: no cover - 协议损坏时的兜底
        return {"ok": False, "error": f"响应解析失败：{exc}"}


def spawn_daemon() -> None:
    st = load_state()
    if daemon_alive(st):
        return
    state_file().unlink(missing_ok=True)
    kwargs: dict[str, Any] = {
        "stdout": subprocess.DEVNULL,
        "stderr": subprocess.DEVNULL,
        "stdin": subprocess.DEVNULL,
        "close_fds": True,
    }
    if os.name == "nt":
        kwargs["creationflags"] = 0x00000008 | 0x00000200  # DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
    else:
        kwargs["start_new_session"] = True
    subprocess.Popen([sys.executable, os.path.abspath(__file__), "_serve"], **kwargs)

    deadline = time.time() + START_TIMEOUT
    while time.time() < deadline:
        if daemon_alive(load_state()):
            return
        time.sleep(0.2)
    die("守护进程启动超时。手动运行 `camoufox-cli doctor` 查看依赖是否齐全。")


# --------------------------------------------------------------------------- #
# 守护进程
# --------------------------------------------------------------------------- #

class Engine:
    """持有 Camoufox 实例，跨命令保持。"""

    def __init__(self) -> None:
        self._cm = None
        self.browser = None
        self.context = None
        self.current = None
        self.meta: dict[str, Any] = {}
        self.tz: str | None = None     # 由 geoip 解析出的时区，供 new_page 使用

    # -- 生命周期 -----------------------------------------------------------
    @property
    def running(self) -> bool:
        return self.browser is not None

    def launch(self, opts: dict[str, Any]) -> str:
        if self.running:
            return "浏览器已在运行"
        try:
            from camoufox.sync_api import Camoufox
        except Exception as exc:
            raise RuntimeError(
                "未安装 camoufox。请执行：\n"
                '  python -m pip install -U "camoufox[geoip]"\n'
                "  python -m camoufox fetch\n"
                f"原始错误：{exc}"
            ) from exc

        kwargs: dict[str, Any] = {"headless": not opts.get("headed", False)}
        if opts.get("os"):
            kwargs["os"] = opts["os"]
        if opts.get("humanize", True):
            kwargs["humanize"] = True
        if opts.get("geoip"):
            kwargs["geoip"] = True
        if opts.get("block_images"):
            kwargs["block_images"] = True
        if opts.get("locale"):
            kwargs["locale"] = opts["locale"]
        proxy_cfg = parse_proxy(opts["proxy"]) if opts.get("proxy") else None
        if proxy_cfg:
            kwargs["proxy"] = proxy_cfg
        if opts.get("preset"):
            # 0.5.x 起支持真实设备指纹预设；旧版本会 TypeError，由下面的降级重试兜住
            kwargs["fingerprint_preset"] = True
        if opts.get("disable_coop"):
            # 允许点击跨域 iframe 里的元素（如 Turnstile 复选框）
            kwargs["disable_coop"] = True
        if opts.get("trust_os_certs"):
            # 走抓包代理（MITM）时必须让浏览器信任系统证书库里的 CA
            prefs = kwargs.setdefault("firefox_user_prefs", {})
            prefs["security.enterprise_roots.enabled"] = True
        if opts.get("window"):
            kwargs["window"] = tuple(opts["window"])

        # 持久 profile：默认不落盘（--isolated 的等价行为），
        # 需要跨会话保留登录态时显式传 --profile。
        if opts.get("profile"):
            kwargs["persistent_context"] = True
            kwargs["user_data_dir"] = str(Path(opts["profile"]).expanduser())
        if opts.get("virtual") and os.name != "nt":
            kwargs["headless"] = "virtual"
            # 老版本 Xvfb 默认 1x1 画布，屏幕指纹会退化成 1x1；这里兜一个正常尺寸。
            os.environ.setdefault("CAMOUFOX_VIRTUAL_DISPLAY_SIZE", "1920x1080x24")

        # camoufox#773：启动级 geoip 不会把时区应用到页面，必须自己解析后传给 context。
        # 只有走代理时才需要反查出口 IP；直连时本机时区就是对的，省掉一次网络请求。
        self.tz = resolve_timezone(proxy_cfg) if (opts.get("geoip") and proxy_cfg) else None
        if self.tz:
            os.environ["TZ"] = self.tz          # 浏览器子进程继承；POSIX 下最直接
            try:
                time.tzset()                     # 仅 POSIX 有；Windows 会 AttributeError
            except Exception:
                pass

        try:
            self._cm = Camoufox(**kwargs)
            self.browser = self._cm.__enter__()
        except TypeError as exc:
            # 不同版本的 Camoufox 接受的参数集合略有差异；剔除可选参数后重试一次，
            # 只保留 headless 这类一定存在的项，避免因为一个锦上添花的开关整个起不来。
            for key in ("locale", "block_images", "window", "os", "humanize",
                        "fingerprint_preset", "disable_coop", "firefox_user_prefs"):
                kwargs.pop(key, None)
            try:
                self._cm = Camoufox(**kwargs)
                self.browser = self._cm.__enter__()
            except Exception:
                raise exc
        ctxs = getattr(self.browser, "contexts", None)
        if opts.get("profile"):
            # persistent_context=True 时返回的就是 BrowserContext，context 选项在创建时
            # 已经固定，无法再改时区——只能靠启动前设置 TZ（POSIX 有效）。
            self.context = self.browser
        elif self.tz:
            # camoufox#773 的正解：新建 context 并显式传 timezone_id，
            # 启动级 geoip 不会把时区应用上去。
            try:
                self.context = self.browser.new_context(timezone_id=self.tz)
            except Exception:
                self.context = ctxs[0] if ctxs else None
        else:
            self.context = ctxs[0] if ctxs else None
        self.meta = {k: v for k, v in opts.items() if v}
        return "已启动 Camoufox（Firefox %s）" % describe_engine_version(self.browser)

    def shutdown(self) -> str:
        try:
            if self._cm is not None:
                self._cm.__exit__(None, None, None)
        except Exception:
            pass
        self._cm = self.browser = self.context = self.current = None
        return "已关闭浏览器"

    # -- 页面 ---------------------------------------------------------------
    def _new_page(self):
        return self.context.new_page()

    def ensure_page(self):
        if not self.context:
            raise RuntimeError("浏览器未启动：先执行 open")
        if self.current is not None and not self.current.is_closed():
            return self.current
        pages = list(self.context.pages)
        self.current = pages[-1] if pages else self._new_page()
        return self.current

    def new_page(self, url: str | None = None):
        if not self.context:
            raise RuntimeError("浏览器未启动：先执行 open")
        page = self._new_page()
        self.current = page
        if url:
            page.goto(url, wait_until="domcontentloaded", timeout=60000)
        return page


def parse_proxy(raw: str) -> dict[str, str]:
    """把 http://user:pass@host:port 拆成 Playwright 需要的 proxy 字典。

    Playwright 的 Firefox 实现不会解析 server 里内嵌的凭据，直接塞给 server 会导致
    407 鉴权失败（症状是「代理连上了但每个请求都 401/407」），必须拆成 username/password。
    """
    raw = raw.strip()
    if "://" not in raw:
        raw = "http://" + raw
    u = urlparse(raw)
    host = u.hostname or ""
    if ":" in host:                      # IPv6 字面量必须带方括号
        host = f"[{host}]"
    out: dict[str, str] = {"server": f"{u.scheme}://{host}" + (f":{u.port}" if u.port else "")}
    if u.username:
        out["username"] = unquote(u.username)
    if u.password:
        out["password"] = unquote(u.password)
    return out


def resolve_timezone(proxy: dict[str, str] | None) -> str | None:
    """geoip 在启动级 API 里只配 locale/坐标/WebRTC，时区要我们自己取（camoufox#773）。

    返回 IANA 时区名；任何一步失败都返回 None（由调用方保持浏览器默认）。
    """
    try:
        from camoufox.geolocation import get_geolocation
        from camoufox.ip import Proxy, public_ip
    except Exception:
        return None
    try:
        if proxy:
            exit_ip = public_ip(Proxy(
                server=proxy["server"],
                username=proxy.get("username"),
                password=proxy.get("password"),
            ).as_string())
        else:
            exit_ip = public_ip()
        return get_geolocation(exit_ip).timezone
    except Exception:
        return None


def describe_engine_version(browser) -> str:
    try:
        return browser.version or "unknown"
    except Exception:
        return "unknown"


class Server:
    def __init__(self) -> None:
        self.engine = Engine()
        self.token = secrets.token_hex(16)
        self.last_seen = time.time()
        self.idle_limit = IDLE_TIMEOUT

    def serve(self) -> None:
        srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        srv.bind(("127.0.0.1", 0))
        srv.listen(8)
        srv.settimeout(1.0)
        port = srv.getsockname()[1]
        write_state_file(port, self.token)

        try:
            while True:
                if time.time() - self.last_seen > self.idle_limit:
                    break
                try:
                    conn, _ = srv.accept()
                except socket.timeout:
                    continue
                self.handle(conn)
        finally:
            srv.close()
            self.engine.shutdown()
            state_file().unlink(missing_ok=True)

    def handle(self, conn: socket.socket) -> None:
        conn.settimeout(CLIENT_TIMEOUT)
        try:
            buf = b""
            while not buf.endswith(b"\n"):
                chunk = conn.recv(65536)
                if not chunk:
                    return
                buf += chunk
            req = json.loads(buf.decode("utf-8", "replace"))
            if not secrets.compare_digest(str(req.get("token", "")), self.token):
                conn.sendall((json.dumps({"ok": False, "error": "令牌无效"}, ensure_ascii=False) + "\n").encode())
                return
            self.last_seen = time.time()
            stop = False
            try:
                out = self.dispatch(req)
                resp = {"ok": True, "out": out}
            except SystemExitAfterResponse as exc:
                resp = {"ok": True, "out": exc.out}
                stop = True
            except Exception as exc:
                resp = {"ok": False, "error": f"{type(exc).__name__}: {exc}"}
            conn.sendall((json.dumps(resp, ensure_ascii=False) + "\n").encode("utf-8"))
            if stop:
                raise SystemExitAfterResponse(resp.get("out", ""))
        except Exception:
            pass
        finally:
            try:
                conn.close()
            except Exception:
                pass

    # -- 命令分发 -----------------------------------------------------------
    def dispatch(self, req: dict[str, Any]) -> str:
        cmd = req.get("cmd", "")
        a = req.get("args") or {}
        e = self.engine

        if cmd == "doctor":
            return doctor_text()

        if cmd == "open":
            try:
                msg = e.launch(a)
            except Exception:
                self.idle_limit = 120  # 启动失败：两分钟没人理就退出，不留僵尸进程
                raise
            page = e.ensure_page()
            if a.get("url"):
                page.goto(a["url"], wait_until="domcontentloaded", timeout=60000)
                msg += f"\n已打开 {page.url}"
            return msg

        if cmd == "close":
            out = e.shutdown()
            raise SystemExitAfterResponse(out)

        if cmd == "goto":
            page = e.ensure_page()
            page.goto(a["url"], wait_until=a.get("wait_until", "domcontentloaded"), timeout=a.get("timeout", 60000))
            return f"已到达 {page.url}"

        if cmd == "snapshot":
            return snapshot_text(e.ensure_page(), limit=a.get("limit", 150))

        if cmd == "click":
            sel = resolve_selector(e.ensure_page(), a["target"])
            e.ensure_page().click(sel, timeout=a.get("timeout", 15000))
            return f"已点击 {a['target']}"

        if cmd == "dblclick":
            sel = resolve_selector(e.ensure_page(), a["target"])
            e.ensure_page().dblclick(sel)
            return f"已双击 {a['target']}"

        if cmd == "fill":
            sel = resolve_selector(e.ensure_page(), a["target"])
            page = e.ensure_page()
            page.fill(sel, a.get("text", ""))
            if a.get("submit"):
                page.press(sel, "Enter")
            return f"已填写 {a['target']}"

        if cmd == "type":
            e.ensure_page().keyboard.type(a.get("text", ""))
            return "已输入"

        if cmd == "press":
            e.ensure_page().keyboard.press(a["key"])
            return f"已按键 {a['key']}"

        if cmd == "hover":
            e.ensure_page().hover(resolve_selector(e.ensure_page(), a["target"]))
            return f"已悬停 {a['target']}"

        if cmd == "select":
            e.ensure_page().select_option(resolve_selector(e.ensure_page(), a["target"]), a["value"])
            return f"已选择 {a['value']}"

        if cmd in ("check", "uncheck"):
            sel = resolve_selector(e.ensure_page(), a["target"])
            (e.ensure_page().check if cmd == "check" else e.ensure_page().uncheck)(sel)
            return f"已{'勾选' if cmd == 'check' else '取消勾选'} {a['target']}"

        if cmd == "eval":
            page = e.ensure_page()
            expr = a["expr"]
            if a.get("target"):
                val = page.eval_on_selector(resolve_selector(page, a["target"]), expr)
            else:
                val = page.evaluate(expr)
            return json.dumps(val, ensure_ascii=False, indent=2) if not isinstance(val, str) else val

        if cmd == "text":
            page = e.ensure_page()
            body = page.inner_text(a.get("target", "body"))
            limit = a.get("limit", 8000)
            return body[:limit] + ("\n…（已截断）" if len(body) > limit else "")

        if cmd == "screenshot":
            page = e.ensure_page()
            out = Path(a.get("filename") or (default_output_dir() / f"shot-{int(time.time())}.png"))
            out.parent.mkdir(parents=True, exist_ok=True)
            if a.get("target"):
                page.locator(resolve_selector(page, a["target"])).screenshot(path=str(out))
            else:
                page.screenshot(path=str(out), full_page=bool(a.get("full")))
            return f"已保存截图：{out}"

        if cmd == "url":
            return e.ensure_page().url

        if cmd == "title":
            return e.ensure_page().title()

        if cmd == "wait":
            page = e.ensure_page()
            if a.get("target"):
                page.wait_for_selector(resolve_selector(page, a["target"]), timeout=a.get("timeout", 30000))
                return "元素已出现"
            page.wait_for_timeout(a.get("ms", 1000))
            return "已等待"

        if cmd == "tab-list":
            return tab_list(e)

        if cmd == "tab-new":
            page = e.new_page(a.get("url"))
            return f"已新建标签页 {page.url}"

        if cmd == "tab-select":
            idx = int(a["index"])
            pages = list(e.context.pages)
            if idx < 0 or idx >= len(pages):
                raise RuntimeError(f"标签页索引越界：{idx}（共 {len(pages)} 个）")
            e.current = pages[idx]
            return f"已切换到标签页 {idx}：{e.current.url}"

        if cmd == "tab-close":
            page = e.ensure_page()
            page.close()
            e.current = None
            return "已关闭当前标签页"

        if cmd == "cookie-list":
            cookies = e.context.cookies()
            if a.get("domain"):
                cookies = [c for c in cookies if a["domain"] in c.get("domain", "")]
            return json.dumps(cookies, ensure_ascii=False, indent=2)

        if cmd == "cookie-set":
            url = a.get("url") or e.ensure_page().url
            cookie = {"name": a["name"], "value": a["value"], "url": url}
            if a.get("domain"):
                cookie.pop("url", None)
                cookie["domain"] = a["domain"]
                cookie["path"] = a.get("path", "/")
            e.context.add_cookies([cookie])
            return "已写入 cookie"

        if cmd == "cookie-clear":
            e.context.clear_cookies()
            return "已清空 cookie"

        if cmd == "localstorage-list":
            return json.dumps(local_storage(e.ensure_page()), ensure_ascii=False, indent=2)

        if cmd == "localstorage-set":
            e.ensure_page().evaluate(
                "(kv) => localStorage.setItem(kv[0], kv[1])", [a["name"], a.get("value", "")]
            )
            return "已写入 localStorage"

        if cmd == "localstorage-clear":
            e.ensure_page().evaluate("() => localStorage.clear()")
            return "已清空 localStorage"

        if cmd == "state-save":
            out = Path(a.get("filename") or (runtime_dir() / "state.json"))
            e.context.storage_state(path=str(out))
            return f"已保存登录态：{out}"

        if cmd == "state-load":
            state = json.loads(Path(a["filename"]).read_text(encoding="utf-8"))
            cookies = state.get("cookies") or []
            if cookies:
                e.context.add_cookies(cookies)
            for origin in state.get("origins") or []:
                page = e.new_page(origin.get("origin"))
                for item in origin.get("localStorage") or []:
                    page.evaluate(
                        "(kv) => { try { localStorage.setItem(kv[0], kv[1]); } catch (e) {} }",
                        [item.get("name"), item.get("value")],
                    )
            return "已载入登录态"

        if cmd == "status":
            if not e.running:
                return "浏览器未启动"
            return json.dumps(
                {
                    "url": e.current.url if e.current and not e.current.is_closed() else None,
                    "tabs": len(e.context.pages) if e.context else 0,
                    "timezone": e.tz,
                    "launch": e.meta,
                },
                ensure_ascii=False,
                indent=2,
            )

        raise RuntimeError(f"未知命令：{cmd}")


class SystemExitAfterResponse(Exception):
    """用于 close：先把结果写回客户端，再退出进程。"""

    def __init__(self, out: str) -> None:
        self.out = out


def write_state_file(port: int, token: str) -> None:
    p = state_file()
    p.write_text(json.dumps({"pid": os.getpid(), "port": port, "token": token}), encoding="utf-8")
    try:
        os.chmod(p, 0o600)
    except Exception:
        pass


# --------------------------------------------------------------------------- #
# 页面操作辅助
# --------------------------------------------------------------------------- #

def resolve_selector(page, target: str) -> str:
    """e12 → [data-cfx-ref="e12"]；其他情况原样当作 CSS 选择器。"""
    t = (target or "").strip()
    if len(t) > 1 and t[0] == "e" and t[1:].isdigit():
        return f'[{REF_ATTR}="{t}"]'
    return t


SNAPSHOT_JS = r"""
(limit) => {
  const sel = 'a,button,input,select,textarea,summary,[role=button],[role=link],[role=checkbox],' +
              '[role=tab],[role=menuitem],[role=switch],[contenteditable="true"]';
  document.querySelectorAll('[' + 'data-cfx-ref' + ']').forEach(e => e.removeAttribute('data-cfx-ref'));
  const out = [];
  let i = 0;
  for (const el of document.querySelectorAll(sel)) {
    const r = el.getBoundingClientRect();
    const st = getComputedStyle(el);
    if ((r.width === 0 && r.height === 0) || st.visibility === 'hidden' || st.display === 'none') continue;
    i += 1;
    const ref = 'e' + i;
    el.setAttribute('data-cfx-ref', ref);
    const raw = el.innerText || el.value || el.getAttribute('aria-label') ||
                el.getAttribute('placeholder') || el.getAttribute('name') || el.id || '';
    const name = String(raw).replace(/\s+/g, ' ').trim().slice(0, 80);
    out.push({
      ref: ref,
      role: el.getAttribute('role') || el.tagName.toLowerCase(),
      name: name,
      type: el.getAttribute('type') || '',
      href: (el.getAttribute('href') || '').slice(0, 120)
    });
    if (out.length >= limit) break;
  }
  return out;
}
"""


def snapshot_text(page, limit: int = 150) -> str:
    nodes = page.evaluate(SNAPSHOT_JS, int(limit))
    if not nodes:
        return "（页面上没有可交互元素）"
    lines = [f"共 {len(nodes)} 个可交互元素："]
    for n in nodes:
        extra = []
        if n.get("type"):
            extra.append(n["type"])
        if n.get("href"):
            extra.append(n["href"])
        suffix = f"  [{', '.join(extra)}]" if extra else ""
        lines.append(f"- {n['ref']} <{n['role']}> {n['name']}{suffix}")
    return "\n".join(lines)


def local_storage(page) -> dict[str, str]:
    return page.evaluate("() => { const o = {}; for (let i = 0; i < localStorage.length; i++) { const k = localStorage.key(i); o[k] = localStorage.getItem(k); } return o; }")


def tab_list(engine: Engine) -> str:
    if not engine.context:
        return "浏览器未启动"
    lines = []
    for idx, page in enumerate(engine.context.pages):
        mark = "*" if page is engine.current else " "
        try:
            title = page.title()
        except Exception:
            title = "(无法读取)"
        lines.append(f"{mark} [{idx}] {title} — {page.url}")
    return "\n".join(lines) if lines else "（没有标签页）"


def doctor_text() -> str:
    lines = []
    lines.append(f"python: {sys.version.split()[0]} ({sys.executable})")
    try:
        import camoufox  # noqa: F401
        from importlib.metadata import version as pkg_version
        try:
            lines.append(f"camoufox: {pkg_version('camoufox')}")
        except Exception:
            lines.append("camoufox: 已安装（版本未知）")
    except Exception:
        lines.append('camoufox: 未安装 → python -m pip install -U "camoufox[geoip]"')
    try:
        from importlib.metadata import version as pkg_version
        lines.append(f"playwright: {pkg_version('playwright')}")
    except Exception:
        lines.append("playwright: 未安装（camoufox 会自带依赖）")
    try:
        from camoufox.pkgman import installed_verstr
        lines.append(f"browser build: {installed_verstr()}")
    except Exception:
        lines.append("browser build: 未知（未执行 camoufox fetch 时会是这样）")
    lines.append(f"timezone: {os.environ.get('TZ') or time.tzname[0]}")
    lines.append(f"runtime dir: {runtime_dir()}")
    lines.append(f"profile dir: {profile_dir()}")
    return "\n".join(lines)


# --------------------------------------------------------------------------- #
# 客户端
# --------------------------------------------------------------------------- #

USAGE = """camoufox-cli —— 基于 Camoufox 的浏览器自动化

会话
  open [url] [--headed] [--os=windows|macos|linux] [--locale=zh-CN] [--geoip]
             [--proxy=http://user:pass@host:port] [--profile=DIR] [--block-images]
             [--preset] [--disable-coop] [--trust-os-certs] [--virtual] [--no-humanize]
  goto URL                     导航
  snapshot                     列出可交互元素并打上 e1/e2 引用
  click e3 | click CSS        点击
  fill e5 "文本" [--submit]    填写
  type "文本" / press Enter    键入
  hover / select / check / uncheck
  eval "document.title" [e5]   执行 JS
  text [e3] [--whole]          取文本
  screenshot [e3] [--full] [--filename=...]
  wait [e3] [--ms=1000]
  tab-list / tab-new [url] / tab-select N / tab-close
  cookie-list [--domain=x] / cookie-set NAME VALUE [--url=...] / cookie-clear
  localstorage-list / localstorage-set NAME VALUE / localstorage-clear
  state-save [file] / state-load file
  status / close / doctor
"""


def parse_flags(argv: list[str]) -> tuple[list[str], dict[str, Any]]:
    pos: list[str] = []
    flags: dict[str, Any] = {}
    for tok in argv:
        if tok.startswith("--"):
            body = tok[2:]
            if "=" in body:
                k, v = body.split("=", 1)
                flags[k] = v
            else:
                flags[body] = True
        else:
            pos.append(tok)
    return pos, flags


def main() -> int:
    if len(sys.argv) > 1 and sys.argv[1] == "_serve":
        srv = Server()
        try:
            srv.serve()
        except SystemExitAfterResponse as exc:  # close 命令
            print(exc.out)
        return 0

    argv = sys.argv[1:]
    if not argv or argv[0] in ("-h", "--help", "help"):
        print(USAGE)
        return 0

    cmd = argv[0]
    pos, flags = parse_flags(argv[1:])

    if cmd == "doctor":
        print(doctor_text())
        return 0

    if cmd == "open":
        spawn_daemon()
        args: dict[str, Any] = {
            "url": pos[0] if pos else None,
            "headed": bool(flags.get("headed")),
            "os": flags.get("os"),
            "humanize": not bool(flags.get("no-humanize")),
            "geoip": bool(flags.get("geoip")),
            "proxy": flags.get("proxy"),
            "profile": flags.get("profile"),
            "block_images": bool(flags.get("block-images")),
            "locale": flags.get("locale"),
            "virtual": bool(flags.get("virtual")),
            "preset": bool(flags.get("preset")),
            "disable_coop": bool(flags.get("disable-coop")),
            "trust_os_certs": bool(flags.get("trust-os-certs")),
        }
        if flags.get("window"):
            try:
                w, h = str(flags["window"]).split("x")
                args["window"] = [int(w), int(h)]
            except Exception:
                die("--window 需要 宽x高，例如 --window=1280x800")
        res = request({"cmd": "open", "args": args})
        print(res.get("out") or res.get("error", ""))
        return 0 if res.get("ok") else 1



    canonical = {
        "goto": "goto", "click": "click", "dblclick": "dblclick", "hover": "hover",
        "check": "check", "uncheck": "uncheck", "select": "select", "fill": "fill",
        "type": "type", "press": "press", "eval": "eval", "text": "text",
        "screenshot": "screenshot", "wait": "wait", "snapshot": "snapshot",
        "tab-list": "tab-list", "tab-new": "tab-new", "tab-select": "tab-select",
        "tab-close": "tab-close", "cookie-list": "cookie-list", "cookie-set": "cookie-set",
        "cookie-clear": "cookie-clear", "localstorage-list": "localstorage-list",
        "localstorage-set": "localstorage-set", "localstorage-clear": "localstorage-clear",
        "state-save": "state-save", "state-load": "state-load", "status": "status",
        "title": "title", "url": "url", "close": "close",
    }
    if cmd not in canonical:
        die(f"未知命令：{cmd}\n\n{USAGE}", 2)

    if not daemon_alive(load_state()) and cmd != "status":
        die("浏览器会话未启动：先执行 `camoufox-cli open`", 1)

    args: dict[str, Any] = {}

    if cmd == "goto":
        args["url"] = pos[0] if pos else ""
        args["wait_until"] = flags.get("wait-until", "domcontentloaded")
    elif cmd in ("click", "dblclick", "hover", "check", "uncheck"):
        args["target"] = pos[0] if pos else ""
    elif cmd == "select":
        args["target"], args["value"] = pos[0], pos[1] if len(pos) > 1 else ""
    elif cmd == "fill":
        args["target"] = pos[0] if pos else ""
        args["text"] = pos[1] if len(pos) > 1 else ""
        args["submit"] = bool(flags.get("submit"))
    elif cmd == "type":
        args["text"] = " ".join(pos)
    elif cmd == "press":
        args["key"] = " ".join(pos) or "Enter"
    elif cmd == "eval":
        args["expr"] = pos[0] if pos else ""
        if len(pos) > 1:
            args["target"] = pos[1]
    elif cmd == "text":
        if pos:
            args["target"] = pos[0]
        args["whole"] = bool(flags.get("whole"))
    elif cmd == "screenshot":
        if pos:
            args["target"] = pos[0]
        args["full"] = bool(flags.get("full"))
        if flags.get("filename"):
            args["filename"] = flags["filename"]
    elif cmd == "wait":
        if pos:
            args["target"] = pos[0]
        if flags.get("ms"):
            args["ms"] = int(flags["ms"])
    elif cmd == "tab-new":
        if pos:
            args["url"] = pos[0]
    elif cmd == "tab-select":
        args["index"] = pos[0] if pos else "0"
    elif cmd == "cookie-list":
        if flags.get("domain"):
            args["domain"] = flags["domain"]
    elif cmd == "cookie-set":
        args["name"] = pos[0]
        args["value"] = pos[1] if len(pos) > 1 else ""
        if flags.get("url"):
            args["url"] = flags["url"]
        if flags.get("domain"):
            args["domain"] = flags["domain"]
    elif cmd == "localstorage-set":
        args["name"] = pos[0]
        args["value"] = pos[1] if len(pos) > 1 else ""
    elif cmd in ("state-save", "state-load"):
        if pos:
            args["filename"] = pos[0]
    elif cmd == "snapshot":
        if flags.get("limit"):
            args["limit"] = int(flags["limit"])

    res = request({"cmd": cmd, "args": args})
    if res.get("ok"):
        out = res.get("out") or ""
        if out:
            print(out)
        return 0
    print(res.get("error", "未知错误"), file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
