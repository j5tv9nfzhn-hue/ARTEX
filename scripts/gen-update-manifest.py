#!/usr/bin/env python3
"""生成 Release 更新清单 latest.json。

为什么需要它：客户端的「自动检测新版本」默认开启。如果每次都打 REST API
（api.github.com/repos/<repo>/releases/latest），未认证额度只有 60 次/小时/IP，
公司内网一个出口后面几十台机器就会互相打爆；而
https://github.com/<repo>/releases/latest/download/latest.json 是网页别名，
不计入 API 配额，所以每个 Release 都要挂一份固定名字的清单。

清单字段与 GitHub Release 同构，selfupdate.FetchLatestManifest 直接反序列化：
  tag_name / name / body / draft / prerelease / published_at / html_url / assets[]
assets[] 里带 name / size / browser_download_url（Go 侧缺 URL 时会自己拼）。

用法（见 .github/workflows/release.yml）：
  python3 scripts/gen-update-manifest.py --dist dist --repo owner/name \
      --tag v1.2.3 --notes dist/notes.md --published-at 2026-01-01T00:00:00Z
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path

# 客户端 parseVersion 只接受纯净数字段：带 -rc.1 / -ko.1 / -global.1 后缀的 tag
# 会被判成「不可比较」，所有客户端的一键更新都会静默禁用。所以这里直接拒绝。
#
# 段数接受 3 或 4：第四段是「同一个 patch 重发的微修订」（2.4.0 -> 2.4.0.1）。
TAG_RE = re.compile(r"^v\d+\.\d+\.\d+(\.\d+)?$")


def sha256_of(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main() -> int:
    ap = argparse.ArgumentParser(description="生成 Release 更新清单 latest.json")
    ap.add_argument("--dist", required=True, help="存放 *.zip 与 SHA256SUMS 的目录")
    ap.add_argument("--repo", required=True, help="owner/name")
    ap.add_argument("--tag", required=True, help="必须形如 v1.2.3 或 v1.2.3.4")
    ap.add_argument("--notes", default="", help="Release 说明正文文件（可省略）")
    ap.add_argument("--published-at", default="", help="RFC3339 发布时间（可省略）")
    ap.add_argument("--out", default="", help="输出路径，默认 <dist>/latest.json")
    args = ap.parse_args()

    if not TAG_RE.match(args.tag):
        print(
            f"拒绝发布：tag {args.tag!r} 不是 vX.Y.Z / vX.Y.Z.W 形式。\n"
            "客户端只认纯数字段版本号（三或四段），带后缀的 tag 会让所有客户端判定为不可比较并禁用一键更新。",
            file=sys.stderr,
        )
        return 2

    dist = Path(args.dist)
    zips = sorted(dist.glob("*.zip"))
    if not zips:
        print(f"拒绝发布：{dist} 里没有任何 .zip 资产。", file=sys.stderr)
        return 2

    # SHA256SUMS 由 release.yml 的 sha256sum 生成；没有就现算，保证两份口径一致。
    sums: dict[str, str] = {}
    sums_file = dist / "SHA256SUMS"
    if sums_file.exists():
        for line in sums_file.read_text(encoding="utf-8").splitlines():
            parts = line.split()
            if len(parts) == 2:
                sums[parts[1].lstrip("*")] = parts[0]

    assets = []
    for z in zips:
        assets.append(
            {
                "name": z.name,
                "size": z.stat().st_size,
                "sha256": sums.get(z.name) or sha256_of(z),
                "browser_download_url": f"https://github.com/{args.repo}/releases/download/{args.tag}/{z.name}",
            }
        )

    # 客户端「清单优先」的检测通道会拿 assets[] 去找 SHA256SUMS 做完整性校验。
    # 清单只列 zip 的话，升级会在校验阶段报「缺少 SHA256SUMS」并拒绝更新——
    # 文件其实就挂在 Release 上，纯粹是清单没写。这里补进去，两条通道口径一致。
    if sums_file.exists():
        assets.append(
            {
                "name": sums_file.name,
                "size": sums_file.stat().st_size,
                "browser_download_url": f"https://github.com/{args.repo}/releases/download/{args.tag}/{sums_file.name}",
            }
        )

    body = ""
    if args.notes:
        notes_path = Path(args.notes)
        if notes_path.exists():
            body = notes_path.read_text(encoding="utf-8", errors="replace").strip()

    # Go 侧 published_at 是 time.Time，空串会让整个清单解码失败，必须给一个合法值。
    published_at = args.published_at.strip() or datetime.now(timezone.utc).isoformat(timespec="seconds")

    manifest = {
        "tag_name": args.tag,
        "name": args.tag,
        "body": body,
        "draft": False,
        "prerelease": False,
        "published_at": published_at,
        "html_url": f"https://github.com/{args.repo}/releases/tag/{args.tag}",
        "assets": assets,
    }

    out = Path(args.out) if args.out else dist / "latest.json"
    out.write_text(json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"已生成 {out}：{len(assets)} 个资产，tag={args.tag}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
