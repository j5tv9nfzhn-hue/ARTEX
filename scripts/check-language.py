#!/usr/bin/env python3
"""检查项目自有文本或静态前端中的韩文残留；简体中文和其他支持的语言均允许。"""

from __future__ import annotations

import argparse
import html
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
# 包含现代音节、组合字母、兼容字母、扩展字母和半角字母。
HANGUL = re.compile(
    r"[\u1100-\u11ff\u3130-\u318f\ua960-\ua97f"
    r"\uac00-\ud7a3\ud7b0-\ud7ff\uffa0-\uffdc]"
)
# 只检查用户可见的 HTML 内容，不把脚本、样式与隐藏注释误作 UI。
HIDDEN_HTML = re.compile(r"<(script|style)\b[^>]*>.*?</\1\s*>", re.I | re.S)
HTML_COMMENTS = re.compile(r"<!--.*?-->", re.S)
HTML_TAGS = re.compile(r"<[^>]+>")


def visible_html(text: str) -> str:
    return html.unescape(HTML_TAGS.sub("", HTML_COMMENTS.sub("", HIDDEN_HTML.sub("", text))))


def source_paths(root: Path) -> list[Path]:
    """同时检查已跟踪和新增文件；Git 忽略的依赖、构建产物及缓存不在检查范围内。"""
    result = subprocess.run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        cwd=root, check=True, stdout=subprocess.PIPE,
    )
    return sorted({root / name.decode("utf-8") for name in result.stdout.split(b"\0") if name})


def scan_files(paths: list[Path], web: bool = False) -> tuple[int, list[tuple[Path, int, str]]]:
    scanned = 0
    violations = []
    for path in paths:
        if not path.is_file():
            continue
        try:
            data = path.read_bytes()
            if b"\0" in data:
                continue
            text = data.decode("utf-8")
        except (UnicodeDecodeError, OSError):
            continue
        scanned += 1
        if web:
            text = visible_html(text)
        for line_number, line in enumerate(text.splitlines(), 1):
            if HANGUL.search(line):
                violations.append((path, line_number, line.strip()[:180]))
    return scanned, violations


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--source", action="store_true", help="检查源码、注释、词库、脚本和文档（默认）")
    mode.add_argument("--web", nargs="?", const=str(ROOT / "web" / "out"), help="检查静态导出的 HTML；默认 web/out")
    args = parser.parse_args(argv)
    if args.web is not None:
        directory = Path(args.web)
        paths = sorted(directory.rglob("*.html"))
        if not paths:
            print(f"未找到静态 HTML：{directory}，请先构建前端。", file=sys.stderr)
            return 2
        label = "静态 HTML"
    else:
        try:
            paths = source_paths(ROOT)
        except (OSError, subprocess.CalledProcessError) as error:
            print(f"无法枚举项目文件：{error}", file=sys.stderr)
            return 2
        label = "项目文本"
    scanned, violations = scan_files(paths, web=args.web is not None)
    for path, line_number, snippet in violations[:100]:
        display = path.relative_to(ROOT) if path.is_relative_to(ROOT) else path
        print(f"{display}:{line_number}: 韩文残留：{snippet}")
    if len(violations) > 100:
        print(f"另有 {len(violations) - 100} 行未展示。")
    print(f"语言检查：扫描 {scanned} 个{label}文件，韩文残留 {len(violations)} 行。")
    return 1 if violations else 0


if __name__ == "__main__":
    raise SystemExit(main())
