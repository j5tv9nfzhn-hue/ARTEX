#!/usr/bin/env python3
"""旧命令的兼容入口：检查静态 HTML 中的韩文残留，不再禁止汉字。"""

from pathlib import Path
import runpy
import sys

if __name__ == "__main__":
    sys.argv[1:] = ["--web", *sys.argv[1:]]
    runpy.run_path(str(Path(__file__).with_name("check-language.py")), run_name="__main__")
