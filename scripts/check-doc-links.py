#!/usr/bin/env python3
"""检查仓库 Markdown 的内部链接、图片和标题锚点。

1. 相对文件链接、Markdown 图片和 HTML img 必须指向存在的文件。
2. #heading 或 other.md#heading 必须对应目标 Markdown 的真实标题。
锚点按 GitHub 规则生成：小写、保留 Word 字符和连字符、空格转连字符；
同名标题依次追加 -1、-2 后缀。无需网络，仅使用 Python 标准库。

存在失效引用时返回 1，可用于 docs.yml 合并门禁和本地验证。
以下不检查：外部 URL；非 Markdown 文件的行号片段；图片的 # 片段；
fenced code block 中的示例链接及标题。外部链接另由独立脚本检查。
执行：python3 -I scripts/check-doc-links.py
"""
import os
import re
import subprocess
import sys
import unicodedata
from collections import defaultdict

# 匹配非图片的 [text](path) 链接；路径到空格为止，不包含可选标题。
MD_LINK = re.compile(r"(?<!\!)\[[^\]]*\]\(([^)\s]+)")
# 匹配 ![alt](path) 图片引用。
MD_IMAGE = re.compile(r"!\[[^\]]*\]\(([^)\s]+)")
# 匹配 HTML <img ... src="path" ...> 图片引用。
HTML_IMAGE = re.compile(r"<img[^>]*\bsrc=[\"']([^\"']+)[\"']", re.IGNORECASE)

# 网络与非文件协议；# 是本文件锚点，不能作为外部协议忽略。
EXTERNAL_PREFIXES = ("http://", "https://", "mailto:", "tel:", "data:")

ATX_HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
# 去除标题中的内联 Markdown，只保留用于生成锚点的纯文本。
MD_INLINE_LINK = re.compile(r"\[([^\]]*)\]\([^)]*\)")


def is_external(target: str) -> bool:
    return target.startswith(EXTERNAL_PREFIXES)


def _is_word_char(ch: str) -> bool:
    """判断 GitHub 锚点规则保留的 Word 字符。

    Ruby 的 Word 类包括 Letter、Mark、Decimal_Number、Connector_Punctuation
    及 Join_Control（U+200C、U+200D）。中文、下划线和变体选择符等均保留。
    """
    if ch == "_":
        return True
    if ch in ("‌", "‍"):
        return True
    category = unicodedata.category(ch)
    if category[0] in ("L", "M"):
        return True
    return category in ("Nd", "Pc")


def slugify(text: str) -> str:
    """将标题转换为 GitHub 锚点 slug，不在此处添加重复后缀。"""
    text = text.strip().lower()
    out = []
    for ch in text:
        if ch == " ":
            out.append("-")
        elif ch == "-" or _is_word_char(ch):
            out.append(ch)
    return "".join(out)


def heading_slugs(root: str, md: str) -> set:
    """收集文件全部 ATX 标题的 GitHub 锚点。

    相同 slug 按出现顺序追加 -1、-2，与 GitHub 行为一致。
    """
    slugs = set()
    counts = defaultdict(int)
    in_fence = False
    with open(os.path.join(root, md), encoding="utf-8", errors="ignore") as fh:
        for line in fh:
            stripped = line.lstrip()
            if stripped.startswith("```") or stripped.startswith("~~~"):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            match = ATX_HEADING.match(line.rstrip("\n"))
            if not match:
                continue
            raw = match.group(2)
            raw = MD_INLINE_LINK.sub(r"\1", raw)
            raw = raw.replace("`", "").replace("*", "").replace("_", "")
            base = slugify(raw)
            seen = counts[base]
            counts[base] += 1
            slugs.add(base if seen == 0 else f"{base}-{seen}")
    return slugs


def main() -> int:
    root = subprocess.check_output(
        ["git", "rev-parse", "--show-toplevel"], text=True
    ).strip()
    tracked = subprocess.check_output(
        ["git", "ls-files"], cwd=root, text=True
    ).splitlines()
    md_files = [f for f in tracked if f.endswith(".md")]

    slug_cache: dict = {}

    def slugs_of(md_path: str) -> set:
        if md_path not in slug_cache:
            slug_cache[md_path] = heading_slugs(root, md_path)
        return slug_cache[md_path]

    checked_files = 0
    checked_anchors = 0
    broken_files = []
    broken_anchors = []
    for md in md_files:
        with open(os.path.join(root, md), encoding="utf-8", errors="ignore") as fh:
            lines = fh.readlines()
        base_dir = os.path.dirname(md)
        in_fence = False
        for lineno, line in enumerate(lines, 1):
            stripped = line.lstrip()
            if stripped.startswith("```") or stripped.startswith("~~~"):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            for pattern in (MD_LINK, MD_IMAGE, HTML_IMAGE):
                for match in pattern.finditer(line):
                    target = match.group(1).strip()
                    if is_external(target):
                        continue

                    if target.startswith("#"):
                        file_part, anchor = "", target[1:]
                    elif "#" in target:
                        file_part, anchor = target.split("#", 1)
                    else:
                        file_part, anchor = target, ""
                    file_part = file_part.split("?", 1)[0]
                    anchor = anchor.strip()

                    # 锚点检查的目标 Markdown 文档：有文件路径则检查该文件；
                    # 只有 #锚点则检查当前文档。
                    target_md = None
                    if file_part:
                        checked_files += 1
                        resolved = os.path.normpath(
                            os.path.join(root, base_dir, file_part)
                        )
                        if not os.path.exists(resolved):
                            broken_files.append((md, lineno, target))
                            continue
                        if file_part.endswith(".md"):
                            target_md = os.path.normpath(
                                os.path.join(base_dir, file_part)
                            )
                    else:
                        target_md = md

                    # 锚点只对普通 Markdown 链接有意义；图片引用的 # 片段跳过。
                    if anchor and target_md is not None and pattern is MD_LINK:
                        checked_anchors += 1
                        if anchor not in slugs_of(target_md):
                            broken_anchors.append((md, lineno, target, target_md))

    print(
        f"检查 {len(md_files)} 个 Markdown 文件、{checked_files} 个文件引用、"
        f"{checked_anchors} 个锚点引用"
    )
    if broken_files:
        print(f"失效文件引用 {len(broken_files)} 个，目标文件不存在：")
        for md, lineno, target in broken_files:
            print(f"  {md}:{lineno} -> {target}")
    if broken_anchors:
        print(f"失效锚点引用 {len(broken_anchors)} 个，目标文档没有该标题：")
        for md, lineno, target, target_md in broken_anchors:
            print(f"  {md}:{lineno} -> {target}  （目标：{target_md}）")
    if broken_files or broken_anchors:
        return 1
    print("失效引用 0，所有内部链接、图片和锚点均指向真实目标。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
