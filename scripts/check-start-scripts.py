#!/usr/bin/env python3
"""校验仓库自带的启动脚本本身没有被写坏。

历史事故：`start-lan.bat` / `start-lan.sh` 曾被 base64 之后的文本覆盖——git 里存的
是一行 `QGVjaG8gb2Zm...`，双击只会得到“不是内部或外部命令”。它藏了好几个版本没人
发现，因为打包脚本只做 cp，坏文件原样进了发布包，而没人真的去双击 `start-lan.*`。

这里把“能被执行”的最低条件钉死：

  - .bat：无 BOM、CRLF、首行 `@echo off`，并且 `chcp 65001` 必须出现在第一处
    非 ASCII 字符之前。UTF-8 无 BOM 的批处理在中文 Windows 上默认按 GBK 逐行解码，
    chcp 之前的中文（哪怕只是 rem）一定是乱码，只是不输出而已；把它放到最前面，
    后面的中文才真的正常。
  - .sh ：无 BOM、LF、首行为解释器（`#!`）。

base64 文本是一整行、没有换行，第一条规则就会把它拦下来。

第二条事故（2026-10-23）：`.bat` 里曾经套着一层 `:loop` 重启循环。两个后果——
端口被占用时每轮都撞同一个端口、无限重启刷屏；Ctrl-C 之后要用户回答
`Terminate batch job (Y/N)?`，按错还会再来一遍。重启已经内置在 exe 里（入口监护
循环 + 5 次/5 分钟防抖），脚本不该再自己循环。所以这里把「脚本必须把前台让给 exe」
钉成结构约束：启动行必须是 `goto #_undefined_#` 的 cmd-shim 写法（npm 同款），
且不许出现自己的重启循环、不许替用户回答 Ctrl-C 询问。
"""

from __future__ import annotations

import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BOM = b"\xef\xbb\xbf"


def is_ascii_line(line: str) -> bool:
    return all(ord(ch) < 128 for ch in line)


def check_bat(path: Path) -> list[str]:
    raw = path.read_bytes()
    errs: list[str] = []
    if raw.startswith(BOM):
        errs.append("带 UTF-8 BOM：cmd 会把 BOM 当成命令的一部分，首行直接报错")
        raw = raw[len(BOM):]
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        return errs + [f"不是合法 UTF-8：{exc}"]

    if "\r\n" not in text:
        errs.append("没有 CRLF 换行：cmd 对 LF-only 的批处理会在 goto/call 上出错")
    elif "\n" in text.replace("\r\n", ""):
        errs.append("混用了裸 LF 与 CRLF")

    lines = text.split("\r\n")
    if not lines or lines[0].strip().lower() != "@echo off":
        got = lines[0][:60] if lines else ""
        errs.append(f"首行必须是 @echo off，实际是 {got!r}")
        # 首行就不对时，后面的行号检查没有意义
        return errs

    chcp_line = None
    first_cjk_line = None
    for i, line in enumerate(lines):
        if chcp_line is None and line.strip().lower().startswith("chcp 65001"):
            chcp_line = i
        if first_cjk_line is None and i > 0 and not is_ascii_line(line):
            first_cjk_line = i

    if chcp_line is None:
        errs.append("缺少 chcp 65001：UTF-8 中文在中文 Windows 上会变成乱码")
    elif first_cjk_line is not None and chcp_line > first_cjk_line:
        errs.append(
            f"chcp 65001 在第 {chcp_line + 1} 行，晚于第 {first_cjk_line + 1} 行的中文："
            "切码之前的行会按系统代码页解码"
        )
    errs.extend(check_bat_structure(lines))
    return errs


def is_rem(line: str) -> bool:
    low = line.strip().lower()
    return low.startswith("rem ") or low == "rem" or low.startswith("::")


def check_bat_structure(lines: list[str]) -> list[str]:
    """钉死"脚本把前台让给 exe"这个结构（见文件头第二条事故）。"""
    errs: list[str] = []
    code = [ln for ln in lines if ln.strip() and not is_rem(ln)]
    joined = "\n".join(code).lower()

    shim = [ln for ln in code if "#_undefined_#" in ln.lower()]
    if not shim:
        errs.append(
            "缺少启动行：必须是 `goto #_undefined_# 2>NUL || \"exe\" %*`（cmd-shim 写法），"
            "否则 Ctrl-C 会弹 Terminate batch job (Y/N)?"
        )
    elif len(shim) > 1:
        errs.append(f"有 {len(shim)} 行 cmd-shim，启动行只能有一处")
    else:
        ln = shim[0].lower()
        if "2>nul" not in ln:
            errs.append("cmd-shim 行缺少 2>NUL：goto 的报错会打到用户脸上")
        if "||" not in ln.split("#_undefined_#", 1)[1]:
            errs.append("cmd-shim 行缺少 ||：goto 之后必须接着跑真正的启动命令")

    if "goto loop" in joined or ":loop" in joined:
        errs.append("脚本自带重启循环（:loop / goto loop）：端口冲突时会无限重启，重启交给 exe")
    if "artex_supervised" in joined:
        errs.append(
            "设置了 ARTEX_SUPERVISED：等于告诉 exe「外面有循环会拉起我」，"
            "exe 会以 75 退出等脚本重启，脚本又没循环，更新后进程就没了"
        )
    if "< nul" in joined or "<nul" in joined:
        errs.append("用 `< nul` 压 Ctrl-C：它只是自动回答 N，脚本会继续往下跑，不是修复")
    return errs


def check_sh(path: Path) -> list[str]:
    raw = path.read_bytes()
    errs: list[str] = []
    if raw.startswith(BOM):
        errs.append("带 UTF-8 BOM：内核会找不到解释器（Bad interpreter）")
        raw = raw[len(BOM):]
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        return errs + [f"不是合法 UTF-8：{exc}"]
    if "\r\n" in text:
        errs.append("含 CRLF：CRLF 会让 shebang 变成 /bin/sh\\r，脚本无法执行")
    first = text.split("\n", 1)[0]
    if not first.startswith("#!"):
        errs.append(f"首行必须是 #! 解释器，实际是 {first[:60]!r}")
    return errs


def main() -> int:
    targets = [
        (ROOT / "start.bat", check_bat),
        (ROOT / "start-lan.bat", check_bat),
        (ROOT / "start.sh", check_sh),
        (ROOT / "start-lan.sh", check_sh),
    ]
    failed = 0
    for path, fn in targets:
        if not path.exists():
            print(f"[start-scripts] 缺少 {path.name}", file=sys.stderr)
            failed += 1
            continue
        errs = fn(path)
        for e in errs:
            print(f"[start-scripts] {path.name}: {e}", file=sys.stderr)
        if errs:
            failed += 1
    if failed:
        print(f"[start-scripts] {failed} 个启动脚本不可用", file=sys.stderr)
        return 1
    print("[start-scripts] 4 个启动脚本格式正常")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
