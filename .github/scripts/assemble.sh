#!/usr/bin/env bash
# assemble（finalize 阶段）：把各平台产物汇总到 ACTION 目录，做完整性/命名/内容校验，
# 生成统一 SHA256SUMS 与 manifest.json，并在配置了密钥时签名 + 回验签名。
#
# 输入（env）：VERSION TAG RELEASE SIGNING COMMIT ACTION_DIR ARTEX_SIGNING_KEY
# 产物：${ACTION_DIR}/<zip>  SHA256SUMS  manifest.json  [SHA256SUMS.sig]
set -euo pipefail

VERSION="${VERSION:?assemble 需要 VERSION}"
TAG="${TAG:-}"
RELEASE="${RELEASE:-false}"
SIGNING="${SIGNING:-none}"
COMMIT="${COMMIT:-unknown}"
ACTION_DIR="${ACTION_DIR:-ACTION}"

err() { echo "::error::$*" >&2; exit 1; }
ok()  { echo "  [ok] $*"; }

[ -d "$ACTION_DIR" ] || err "产物目录不存在：${ACTION_DIR}"
cd "$ACTION_DIR"

# ── 1. 期望清单来自 plan.json（保证「校验什么就产出什么」）─────────────
[ -f plan.json ] || err "缺少 plan.json：finalize 必须消费 plan 阶段的结论"
mapfile -t EXPECTED < <(python3 -c '
import json
plan = json.load(open("plan.json"))
for p in plan["six_pack"]:
    if p["selected"]:
        print(p["package"])
')

echo "[assemble] 期望产物 ${#EXPECTED[@]} 个"

missing=0
for pkg in "${EXPECTED[@]}"; do
  [ -f "$pkg" ] || { echo "::error::缺少产物 ${pkg}"; missing=1; }
done
[ "$missing" -eq 0 ] || err "产物不齐，终止"

# ── 2. 归档完整性与内容核对 ─────────────────────────────────────────
for pkg in "${EXPECTED[@]}"; do
  unzip -tq "$pkg" >/dev/null 2>&1 || err "归档损坏：${pkg}"
  listing="$(unzip -Z1 "$pkg")"
  root="${pkg%.zip}"
  case "$pkg" in
    *windows*)
      printf '%s\n' "$listing" | grep -Fxq "${root}/artex.exe" || err "${pkg} 缺少 artex.exe"
      printf '%s\n' "$listing" | grep -Fxq "${root}/start.bat" || err "${pkg} 缺少 start.bat"
      ;;
    *)
      printf '%s\n' "$listing" | grep -Fxq "${root}/artex" || err "${pkg} 缺少 artex"
      printf '%s\n' "$listing" | grep -Fxq "${root}/start.sh" || err "${pkg} 缺少 start.sh"
      ;;
  esac
  # 每个包都必须带上 skills 与示例配置，否则解压即不可用
  printf '%s\n' "$listing" | grep -q "${root}/skills/" || err "${pkg} 缺少 skills/"
  printf '%s\n' "$listing" | grep -Fxq "${root}/config.example.json" || err "${pkg} 缺少 config.example.json"
  ok "${pkg} 归档完整"
done

# ── 3. 统一校验和 ───────────────────────────────────────────────────
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "${EXPECTED[@]}" | sort -k2 > SHA256SUMS
else
  shasum -a 256 "${EXPECTED[@]}" | sort -k2 > SHA256SUMS
fi
sha256sum -c SHA256SUMS >/dev/null 2>&1 || shasum -a 256 -c SHA256SUMS >/dev/null 2>&1 \
  || err "SHA256SUMS 自检失败"
ok "SHA256SUMS 已生成并自检通过"

# ── 4. 签名（可选）＋ 回验 --------------------------------------------------
if [ "$SIGNING" = "pem" ]; then
  [ -n "${ARTEX_SIGNING_KEY:-}" ] || err "signing=pem 但 ARTEX_SIGNING_KEY 为空"
  command -v openssl >/dev/null 2>&1 || err "signing=pem 但缺少 openssl"

  umask 077
  keyfile="$(mktemp)"; pubfile="$(mktemp)"
  trap 'rm -f "$keyfile" "$pubfile"' EXIT
  printf '%s' "$ARTEX_SIGNING_KEY" > "$keyfile"

  openssl dgst -sha256 -sign "$keyfile" -out SHA256SUMS.sig SHA256SUMS || err "签名失败"
  openssl rsa -in "$keyfile" -pubout -out "$pubfile" >/dev/null 2>&1 || err "无法导出公钥"
  openssl dgst -sha256 -verify "$pubfile" -signature SHA256SUMS.sig SHA256SUMS >/dev/null 2>&1 \
    || err "签名回验失败"
  ok "SHA256SUMS.sig 已签名并通过回验"
else
  echo "  [--] 未配置签名密钥：跳过签名（仅 SHA256SUMS）"
fi

# ── 5. manifest.json ────────────────────────────────────────────────
export MAN_VERSION="$VERSION" MAN_TAG="$TAG" MAN_RELEASE="$RELEASE" \
       MAN_SIGNING="$SIGNING" MAN_COMMIT="$COMMIT"
python3 - <<'PY'
import hashlib, json, os
pkgs = sorted(f for f in os.listdir(".") if f.endswith(".zip"))
entries = []
for f in pkgs:
    h = hashlib.sha256()
    with open(f, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    entries.append({"package": f, "sha256": h.hexdigest(), "bytes": os.path.getsize(f)})
manifest = {
    "stage": "job/finalize",
    "version": os.environ["MAN_VERSION"],
    "tag": os.environ["MAN_TAG"],
    "release": os.environ["MAN_RELEASE"] == "true",
    "signing": os.environ["MAN_SIGNING"],
    "commit": os.environ["MAN_COMMIT"],
    "package_count": len(entries),
    "packages": entries,
}
json.dump(manifest, open("manifest.json", "w"), indent=2, ensure_ascii=False)
print(f"  [ok] manifest.json：{len(entries)} 个包")
PY

echo "[assemble] ACTION 目录内容："
ls -la
