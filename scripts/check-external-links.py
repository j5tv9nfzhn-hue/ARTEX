#!/usr/bin/env python3
"""检查已跟踪 Markdown 所引用的外部 HTTP、HTTPS 链接。

check-doc-links.py 只检查内部引用，避免网络依赖导致合并门禁不稳定。
本脚本负责周期或手动检查公开文档已有的参照链接，不进行目标扫描或探测。

提取普通链接、图片、自动链接及裸 URL；跳过 fenced code block 和内联代码，
因为其中常为命令、配置或示例目标，而非读者可跟随的参照链接。
排除 localhost、私有/环回/链路本地 IP、RFC 2606/6761 示例与保留域名、
无点的非 FQDN 名称。仅使用 Python 标准库。

使用浏览器 User-Agent、GET 并跟随重定向，避免将拒绝 HEAD 的站点误判为失效。
OK：最终 2xx、3xx。
RESTRICTED：401、403、405、429，主机存活但检查方式被访问政策或限流阻止。
DOWN：404、410、重试后 5xx，以及 DNS、连接、超时、SSL 错误。
网络错误、5xx、429 会延迟重试，区分暂时故障与持续不可用。

external-links-allowlist.txt 记录无法修复的上游既有 DOWN 链接，标为 ALLOWED，
不计入 strict 失败。RESTRICTED 也不导致失败；新出现的非例外 DOWN 才失败。
默认仅报告并返回 0，周期或发布检查可添加 --strict；--list 仅列举来源，不访问网络。
执行：python3 -I scripts/check-external-links.py
"""
import argparse
import http.client
import os
import re
import socket
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request
from collections import defaultdict
from urllib.parse import urlsplit

# 正文 [text](url) 链接和 ![alt](url) 图片；URL 到空格或右括号为止。
MD_LINK = re.compile(r"(?<!\!)\[[^\]]*\]\(([^)\s]+)")
MD_IMAGE = re.compile(r"!\[[^\]]*\]\(([^)\s]+)")
# 匹配 <https://...> 自动链接。
AUTOLINK = re.compile(r"<(https?://[^>\s]+)>")
# 匹配正文中的裸 URL，稍后剥离尾部标点。
BARE_URL = re.compile(r"https?://[^\s)>\]\"'`]+")
# 提取前，将单反引号内联代码片段替换为空格。
INLINE_CODE = re.compile(r"`[^`]*`")
# 正文 URL 尾部常见的标点。
TRAILING_PUNCT = ".,;:!?\"'»)]}>"

# IPv4 私有、环回和链路本地地址段；172.16~31 使用独立模式。
PRIVATE_IPV4 = re.compile(r"^(127\.|10\.|192\.168\.|169\.254\.|0\.0\.0\.0$)")
PRIVATE_IPV4_172 = re.compile(r"^172\.(1[6-9]|2\d|3[01])\.")

BROWSER_UA = (
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
    "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"
)

# 主机存活，但服务器政策阻止当前验证方式的状态码，不视为链接损坏。
RESTRICTED_CODES = {401, 403, 405, 429}

ALLOWLIST_PATH = os.path.join("scripts", "external-links-allowlist.txt")


def load_allowlist(root: str) -> set:
    """读取无法修复的已知 DOWN URL 集合，缺少文件时返回空集合。

    每行一个 URL，# 后为注释，忽略空行与纯注释行。
    """
    path = os.path.join(root, ALLOWLIST_PATH)
    allow = set()
    if not os.path.exists(path):
        return allow
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.split("#", 1)[0].strip()
            if line:
                allow.add(line)
    return allow


def strip_code(line: str) -> str:
    """移除代码围栏之外的内联代码，内部 URL 不属于参照链接。"""
    return INLINE_CODE.sub(" ", line)


def is_checkable(url: str) -> bool:
    """过滤保留或占位主机，只检查实际外部 URL。"""
    parts = urlsplit(url)
    if parts.scheme not in ("http", "https"):
        return False
    host = parts.hostname
    if not host:
        return False
    host = host.lower()
    if host == "localhost" or host.endswith(".localhost"):
        return False
    if host == "::1":
        return False
    if PRIVATE_IPV4.match(host) or PRIVATE_IPV4_172.match(host):
        return False
    if host in ("example.com", "example.org", "example.net", "example.edu"):
        return False
    if host.endswith((".example.com", ".example.org", ".example.net", ".example")):
        return False
    if host.endswith((".test", ".invalid", ".local", ".localdomain", ".tld")):
        return False
    if "." not in host:  # 非 FQDN 裸主机名，如 target。
        return False
    return True


def extract(root: str, md_files: list) -> dict:
    """从已跟踪 Markdown 构建 URL 到文件、行号来源的映射。"""
    found = defaultdict(list)
    for md in md_files:
        with open(os.path.join(root, md), encoding="utf-8", errors="ignore") as fh:
            lines = fh.readlines()
        in_fence = False
        for lineno, raw in enumerate(lines, 1):
            stripped = raw.lstrip()
            if stripped.startswith("```") or stripped.startswith("~~~"):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            line = strip_code(raw)
            candidates = []
            for pattern in (MD_LINK, MD_IMAGE, AUTOLINK):
                candidates.extend(m.group(1) for m in pattern.finditer(line))
            for m in BARE_URL.finditer(line):
                candidates.append(m.group(0).rstrip(TRAILING_PUNCT))
            for url in candidates:
                url = url.strip().rstrip(TRAILING_PUNCT)
                if is_checkable(url):
                    where = (md, lineno)
                    if where not in found[url]:
                        found[url].append(where)
    return found


def probe(url: str, timeout: float, retries: int, delay: float):
    """以浏览器 User-Agent 发送 GET，返回分类和诊断详情。

    分类为 OK、RESTRICTED、DOWN；网络错误、5xx、429 支持重试。
    """
    req = urllib.request.Request(
        url,
        method="GET",
        headers={
            "User-Agent": BROWSER_UA,
            "Accept": "*/*",
            "Accept-Language": "ko,en;q=0.8",
        },
    )
    last = ""
    for attempt in range(retries + 1):
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                code = resp.getcode()
                if code in RESTRICTED_CODES:
                    return "RESTRICTED", f"HTTP {code}"
                return "OK", f"HTTP {code}"
        except urllib.error.HTTPError as exc:
            code = exc.code
            if code in RESTRICTED_CODES:
                return "RESTRICTED", f"HTTP {code}"
            if code >= 500 or code == 408:
                last = f"HTTP {code}"  # 错误可能为暂时状态，允许重试。
            else:
                return "DOWN", f"HTTP {code}"  # 404、410 等为确定错误，无需重试。
        except (
            urllib.error.URLError,
            http.client.HTTPException,
            ssl.SSLError,
            socket.timeout,
            ConnectionError,
            TimeoutError,
            OSError,
        ) as exc:
            reason = getattr(exc, "reason", exc)
            last = f"{type(exc).__name__}: {reason}"
        if attempt < retries:
            time.sleep(delay * (attempt + 1))
    return "DOWN", last


def main() -> int:
    ap = argparse.ArgumentParser(description="检查已跟踪 Markdown 的外部链接状态")
    ap.add_argument("--strict", action="store_true",
                    help="存在非例外 DOWN 时返回 1，用于周期或发布检查")
    ap.add_argument("--list", action="store_true",
                    help="仅列举 URL 与来源，不发送网络请求")
    ap.add_argument("--timeout", type=float, default=15.0, help="请求超时（秒）")
    ap.add_argument("--retries", type=int, default=2, help="网络错误、5xx、429 重试次数")
    ap.add_argument("--delay", type=float, default=0.5, help="调用与重试的基础间隔（秒）")
    args = ap.parse_args()

    root = subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], text=True
    ).strip()
    tracked = subprocess.check_output(["git", "ls-files"], cwd=root, text=True).splitlines()
    md_files = [f for f in tracked if f.endswith(".md")]

    allow = load_allowlist(root)
    found = extract(root, md_files)
    urls = sorted(found)
    print(f"从 {len(md_files)} 个 Markdown 文件收集 {len(urls)} 个待检查 URL")

    if args.list:
        for url in urls:
            where = ", ".join(f"{f}:{ln}" for f, ln in found[url])
            tag = " [allowlist]" if url in allow else ""
            print(f"  {url}{tag}  ({where})")
        return 0

    results = {"OK": [], "RESTRICTED": [], "ALLOWED": [], "DOWN": []}
    for i, url in enumerate(urls):
        if i:
            time.sleep(args.delay)  # 调用间略微延迟，避免触发环境或远端的限流。
        verdict, detail = probe(url, args.timeout, args.retries, args.delay)
        if verdict == "DOWN" and url in allow:
            verdict = "ALLOWED"  # 无法修复、继承自上游的已知 DOWN 链接。
        results[verdict].append((url, detail))
        print(f"  [{verdict:10}] {url} — {detail}")

    print(
        f"\n摘要：OK {len(results['OK'])} · RESTRICTED {len(results['RESTRICTED'])} · "
        f"ALLOWED {len(results['ALLOWED'])} · DOWN {len(results['DOWN'])}"
    )
    if results["RESTRICTED"]:
        print("RESTRICTED（主机存活，验证方式受限，不视为链接损坏）：")
        for url, detail in results["RESTRICTED"]:
            print(f"  {url} — {detail}")
    if results["ALLOWED"]:
        print("ALLOWED（允许列表内的已知 DOWN，无法修复，不计入 strict 失败）：")
        for url, detail in results["ALLOWED"]:
            where = ", ".join(f"{f}:{ln}" for f, ln in found[url])
            print(f"  {url} — {detail}  （来源：{where}）")
    if results["DOWN"]:
        print("DOWN（可能失效，需要确认）：")
        for url, detail in results["DOWN"]:
            where = ", ".join(f"{f}:{ln}" for f, ln in found[url])
            print(f"  {url} — {detail}  （来源：{where}）")

    if args.strict and results["DOWN"]:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
