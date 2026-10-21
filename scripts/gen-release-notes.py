#!/usr/bin/env python3
"""生成 Release 发布说明（完整变更）。

为什么不用 `gh release create --generate-notes`：
GitHub 的自动说明只汇总 **PR 标题**。本仓库大量改动是直接提交（没有 PR），
自动生成的结果常常只剩两三条，等于没写——v2.2.0 首发就踩到了这一点。
这里改为直接读 git 历史：从「上一个 tag」到当前 tag 的全部提交，按
conventional commit 前缀分组，输出中文小标题。

体积限制：Release 正文会被写进 latest.json，客户端每次版本检测都会下载它，
所以正文不能无限膨胀。默认上限 8 KB，超出时按「新增 > 修复 > 其余」的优先级
截断，并注明「其余 N 条见提交历史」。

用法（见 .github/workflows/release.yml）：
  python3 -I scripts/gen-release-notes.py \
      --repo owner/name --tag v2.2.0 --out dist/RELEASE_NOTES.md [--dist dist]
"""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
from pathlib import Path

# 展示顺序即优先级：先新增、再修复，其余按重要性递减。
GROUPS: list[tuple[str, str]] = [
    ("feat", "新增功能"),
    ("fix", "修复"),
    ("perf", "性能"),
    ("refactor", "重构"),
    ("ci", "构建与发布"),
    ("build", "构建与发布"),
    ("docs", "文档"),
    ("test", "测试"),
    ("style", "样式与格式"),
    ("chore", "杂项"),
    ("other", "其他"),
]

# 每个版本在正文顶部的定位说明。内容型版本必须写清楚「这一版做到了什么程度」，
# 避免读者把「第一版」当成「已经改完」。
VERSION_NOTES: dict[str, str] = {
    "v2.2.2": (
        "**架构优化第一版**：本版把「五断层收口」与流式链路的第一轮改造全部落地——"
        "存储保底、活动流自动保留、系统浏览器 L1、思考/正文/工具实时流式、"
        "流式 markdown 增量渲染。**不保证完全优化完整**：`tool_usage` 清理规则、"
        "L2 Firefox 加固档、工具结果按类型折叠等仍留待后续版本。"
    ),
    "v2.2.3": (
        "**架构与框架第二阶段（P2+P3）**：独立复核（verifier）成为链路上的一等角色——"
        "发现落库后按「高危强制复核 + 中低危采样」进入复核队列，由独立会话、只读工具的"
        "复核员给出 `verified / refuted / inconclusive`；只有 `verified` 计入资产真值与"
        "报告正文，`inconclusive` 与未写结论一律保持未复核（发现列表与详情页均已接入"
        "复核态徽标、发起入口与历史证据）。同时把 `db/dialect_*` 中读起来像还存在"
        "第二条实现的 PostgreSQL 残留整体删除，心智模型统一为 SQLite 单引擎。"
    ),
}

# type(scope)!: subject
COMMIT_RE = re.compile(r"^(?P<type>[a-zA-Z]+)(?:\((?P<scope>[^)]*)\))?!?:\s*(?P<subject>.+)$")


def git(*args: str) -> str:
    proc = subprocess.run(["git", *args], capture_output=True, text=True)
    if proc.returncode != 0:
        raise SystemExit(f"git {' '.join(args)} 失败：{proc.stderr.strip()}")
    return proc.stdout


def single_line(text: str) -> str:
    return " ".join(text.split())


def resolve_previous_tag(tag: str) -> str:
    """找当前 tag 的父提交上最近的 vX.Y.Z tag；找不到说明这是首个发行版。"""
    try:
        out = git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", f"{tag}^").strip()
    except SystemExit:
        return ""
    # describe 可能返回当前 tag 自身（tag 与父提交同点时才会发生），那时视为无前序版本。
    return "" if out == tag else out


def read_commits(rng: str) -> list[tuple[str, str]]:
    rows: list[tuple[str, str]] = []
    for line in git("log", "--no-merges", "--pretty=format:%h%x09%s", rng).splitlines():
        if "\t" not in line:
            continue
        sha, subject = line.split("\t", 1)
        subject = single_line(subject)
        if not subject:
            continue
        rows.append((sha.strip(), subject))
    return rows


def classify(subject: str) -> tuple[str, str, str]:
    m = COMMIT_RE.match(subject)
    if not m:
        return "other", "", subject
    kind = m.group("type").lower()
    if kind not in {k for k, _ in GROUPS}:
        return "other", "", subject
    scope = (m.group("scope") or "").strip()
    return kind, scope, single_line(m.group("subject"))


def human_size(n: int) -> str:
    return f"{n / (1024 * 1024):.1f} MB"


def build_assets_section(dist: Path, repo: str, tag: str) -> str:
    zips = sorted(dist.glob("*.zip"))
    if not zips:
        return ""
    lines = ["## 发行产物", "", "| 平台 | 文件 | 大小 |", "|---|---|---|"]
    for z in zips:
        # artex-rea-2.2.0-linux-amd64.zip
        parts = z.stem.split("-")
        if len(parts) >= 2:
            os_name = {"linux": "Linux", "darwin": "macOS", "windows": "Windows"}.get(parts[-2], parts[-2])
            plat = f"{os_name} {parts[-1]}"
        else:
            plat = z.stem
        lines.append(f"| {plat} | [`{z.name}`]({repo_url(repo)}/releases/download/{tag}/{z.name}) | {human_size(z.stat().st_size)} |")
    if (dist / "SHA256SUMS").exists():
        lines.append("")
        lines.append("另附 `SHA256SUMS`（各包校验和）与 `latest.json`（自动更新清单）。")
    return "\n".join(lines)


def repo_url(repo: str) -> str:
    return f"https://github.com/{repo}"


def main() -> int:
    ap = argparse.ArgumentParser(description="生成 Release 发布说明（完整变更）")
    ap.add_argument("--repo", required=True, help="owner/name")
    ap.add_argument("--tag", required=True, help="当前 tag，如 v2.2.0")
    ap.add_argument("--out", required=True, help="输出的 Markdown 路径")
    ap.add_argument("--from", dest="from_tag", default="", help="起始 tag（默认自动探测上一个 vX.Y.Z）")
    ap.add_argument("--dist", default="", help="产物目录，用于列出 zip 与大小（可省略）")
    ap.add_argument("--max-bytes", type=int, default=8192, help="正文体积上限（UTF-8 字节，默认 8192）")
    args = ap.parse_args()

    version = args.tag[1:] if args.tag.startswith("v") else args.tag
    prev = args.from_tag or resolve_previous_tag(args.tag)
    if prev and prev == args.tag:
        prev = ""

    rng = f"{prev}..{args.tag}" if prev else args.tag
    rows = read_commits(rng)
    if not rows:
        print(f"警告：{rng} 区间内没有提交，生成占位说明。", file=sys.stderr)

    buckets: dict[str, list[tuple[str, str, str]]] = {}
    for sha, subject in rows:
        kind, scope, title = classify(subject)
        buckets.setdefault(kind, []).append((sha, scope, title))

    counts = {k: len(v) for k, v in buckets.items()}
    headline_bits = []
    for key, label in (("feat", "项新增"), ("fix", "项修复")):
        if counts.get(key):
            headline_bits.append(f"{counts[key]} {label}")
    tail = [f"{counts[k]} 项{label}" for k, label in (("refactor", "重构"), ("ci", "构建改动"), ("docs", "文档")) if counts.get(k)]

    header = [f"# ARTEX-REA {args.tag}", ""]
    if prev:
        summary = f"> 自 `{prev}` 起共 **{len(rows)}** 个提交"
    else:
        summary = f"> 首个发行版，共 **{len(rows)}** 个提交"
    if headline_bits:
        summary += "：" + "、".join(headline_bits)
    if tail:
        summary += "；另有 " + "、".join(tail)
    header.append(summary + "。")
    note = VERSION_NOTES.get(args.tag, "")
    if note:
        header.extend(["", note])
    header.append("")

    footer_lines = []
    if args.dist:
        assets = build_assets_section(Path(args.dist), args.repo, args.tag)
        if assets:
            footer_lines += ["", "---", "", assets]
    footer_lines += [
        "",
        "## 校验与升级",
        "",
        "```bash",
        "sha256sum -c SHA256SUMS",
        "```",
        "",
        "解压后覆盖可执行文件，用 `start.sh`（Linux / macOS）或 `start.bat`（Windows）启动；"
        "退出码 75 表示需要重启，启动脚本会自动拉起新版本。",
        "",
        f"完整提交历史：{repo_url(args.repo)}/commits/{args.tag}",
        "",
    ]

    header_text = "\n".join(header)
    footer_text = "\n".join(footer_lines)
    budget = args.max_bytes - len((header_text + footer_text).encode("utf-8"))

    # 同一标题可能对应多个前缀（ci / build），按展示标题合并。
    sections: list[tuple[str, list[str]]] = []
    seen_titles: dict[str, int] = {}
    for key, label in GROUPS:
        items = buckets.get(key) or []
        if not items:
            continue
        if label in seen_titles:
            idx = seen_titles[label]
            sections[idx][1].extend(items)  # type: ignore[index]
        else:
            seen_titles[label] = len(sections)
            sections.append((label, list(items)))

    body: list[str] = []
    omitted = 0
    for label, items in sections:
        lines: list[str] = []
        used = 0
        shown = 0
        for sha, scope, title in items:
            line = f"- **{scope}** {title} (`{sha}`)" if scope else f"- {title} (`{sha}`)"
            size = len((line + "\n").encode("utf-8"))
            if budget - used - size < 0:
                break
            lines.append(line)
            used += size
            shown += 1
        if shown == 0:
            # 预算已耗尽：整个分类都放不下，只累计条数，末尾统一说明。
            omitted += len(items)
            continue
        if shown < len(items):
            rest = len(items) - shown
            lines.append(f"- …其余 {rest} 条见提交历史")
            used += 80
        body.append(f"## {label}")
        body.append("")
        body.extend(lines)
        body.append("")
        budget -= used

    if omitted:
        body.append(f"> 另有 {omitted} 条变更未在正文列出（受正文体积上限限制），见下方提交历史。")
        body.append("")

    text = header_text + "\n" + "\n".join(body) + footer_text
    Path(args.out).parent.mkdir(parents=True, exist_ok=True)
    Path(args.out).write_text(text.rstrip() + "\n", encoding="utf-8")
    print(f"已生成 {args.out}：{len(rows)} 个提交，{len(text.encode('utf-8'))} 字节（上限 {args.max_bytes}）")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
