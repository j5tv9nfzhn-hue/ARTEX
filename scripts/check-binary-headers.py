#!/usr/bin/env python3
"""校验构建产物的可执行文件头与目标架构（PE / ELF / Mach-O）；ci.yml 与 release.yml 共用。

用法：
    python3 -I scripts/check-binary-headers.py            # 校验全部六目标
    python3 -I scripts/check-binary-headers.py linux/amd64 darwin/arm64 ...

目标文件为 dist/artex-<os>-<arch>/artex（Windows 为 artex.exe）。runner
不能执行非宿主平台的二进制，这些检查代替冒烟测试；任何一项不符即非零退出。
"""

from __future__ import annotations

import struct
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

# 支持的目标与期望的机器类型码。
TARGETS = {
    "linux": {"amd64": 0x3E, "arm64": 0xB7},
    "darwin": {"amd64": 0x01000007, "arm64": 0x0100000C},
    "windows": {"amd64": 0x8664, "arm64": 0xAA64},
}


def check(spec: str) -> None:
    """校验单个 OS/ARCH 目标的文件头与机器类型。"""
    goos, _, arch = spec.partition("/")
    known = "、".join(f"{os_}/{arch_}" for os_, arches in TARGETS.items() for arch_ in arches)
    if arch not in TARGETS.get(goos, {}):
        sys.exit(f"不支持的目标：{spec}（支持 {known}）")
    binary = ROOT / "dist" / f"artex-{goos}-{arch}" / ("artex.exe" if goos == "windows" else "artex")
    if not binary.is_file():
        sys.exit(f"{binary}: 文件不存在（先构建再校验）")
    with binary.open("rb") as handle:
        data = handle.read(4096)
    if goos == "windows":
        if data[:2] != b"MZ":
            sys.exit(f"{binary}: 不是 Windows PE（缺少 MZ 头）")
        pe = struct.unpack_from("<I", data, 0x3C)[0]
        if data[pe : pe + 4] != b"PE\x00\x00":
            sys.exit(f"{binary}: 缺少 PE\\0\\0 签名")
        machine = struct.unpack_from("<H", data, pe + 4)[0]
    elif goos == "linux":
        if data[:4] != b"\x7fELF" or data[4] != 2:
            sys.exit(f"{binary}: 不是 64 位 ELF")
        machine = struct.unpack_from("<H", data, 18)[0]
    else:
        if data[:4] != b"\xcf\xfa\xed\xfe":
            sys.exit(f"{binary}: 不是 64 位 thin Mach-O")
        machine = struct.unpack_from("<I", data, 4)[0]
    want = TARGETS[goos][arch]
    if machine != want:
        sys.exit(f"{binary}: 机器类型 0x{machine:X} 与目标 {goos}/{arch} (0x{want:X}) 不符")
    print(f"结构校验通过：{binary} machine=0x{machine:X} ({goos}/{arch})")


def main(argv: list[str]) -> int:
    specs = argv or [f"{goos}/{arch}" for goos, arches in TARGETS.items() for arch in arches]
    for spec in specs:
        check(spec)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
