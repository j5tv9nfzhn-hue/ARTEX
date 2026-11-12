#!/usr/bin/env bash
# plan：三端六包校验（清单 / 工具链 / 命名标签 / 版本 / 签名）+ 生成构建矩阵。
# 只读校验、不编译。任何一项不通过即失败 —— 这是「能不能开工」的硬闸门。
#
# 输入（env）：PLATFORM VERSION RELEASE TAG SIGNING ACTION_DIR
# 输出：$GITHUB_OUTPUT(matrix=<json>, packages=<n>) + ${ACTION_DIR}/plan.json
set -euo pipefail

PLATFORM="${PLATFORM:-all}"
VERSION="${VERSION:?plan 需要 VERSION}"
RELEASE="${RELEASE:-false}"
TAG="${TAG:-}"
SIGNING="${SIGNING:-none}"
ACTION_DIR="${ACTION_DIR:-ACTION}"

fail=0
bad()  { echo "::error::$*"; fail=1; }
ok()   { echo "  [ok] $*"; }
hint() { echo "  [--] $*"; }

OS_LIST=(linux darwin windows)
ARCH_LIST=(amd64 arm64)

echo "[plan] 三端六包校验（3 平台 × 2 架构 = 6 包），版本 ${VERSION}"

# ── ① 清单完整性：必须恰好 3×2=6 ─────────────────────────────────────
EXPECTED=()
for os in "${OS_LIST[@]}"; do
  for arch in "${ARCH_LIST[@]}"; do
    EXPECTED+=("${os}/${arch}")
  done
done
if [ "${#EXPECTED[@]}" -ne 6 ]; then
  bad "六包清单构造异常：得到 ${#EXPECTED[@]} 个"
fi
ok "六包清单：${EXPECTED[*]}"

# ── ② 工具链交叉核对 ────────────────────────────────────────────────
dist_list="$(go tool dist list || true)"
for t in "${EXPECTED[@]}"; do
  if printf '%s\n' "$dist_list" | grep -Fxq "$t"; then
    ok "工具链支持 ${t}"
  else
    bad "工具链不支持 ${t}（不在 go tool dist list 中）"
  fi
done

# ── ③ 每端恰好两包 ──────────────────────────────────────────────────
for os in "${OS_LIST[@]}"; do
  n=0
  for t in "${EXPECTED[@]}"; do [ "${t%%/*}" = "$os" ] && n=$((n + 1)); done
  if [ "$n" -eq 2 ]; then ok "${os} 端 2 包"; else bad "${os} 端 ${n} 包（应为 2）"; fi
done

# ── ④ 标签 / 产物命名 ──────────────────────────────────────────────
NAME_RE='^artex-[0-9A-Za-z][0-9A-Za-z.+-]*-(linux|darwin|windows)-(amd64|arm64)\.zip$'
PKGS=()
for t in "${EXPECTED[@]}"; do
  os="${t%%/*}"
  arch="${t##*/}"
  pkg="artex-${VERSION}-${os}-${arch}.zip"
  PKGS+=("$pkg")
  if [[ "$pkg" =~ $NAME_RE ]]; then
    ok "产物标签 ${pkg}"
  else
    bad "产物命名不合规：${pkg}"
  fi
  # 归档内可执行文件名必须与目标系统一致
  if [ "$os" = "windows" ]; then
    hint "${t} 归档内含 artex.exe + start.bat"
  else
    hint "${t} 归档内含 artex + start.sh"
  fi
done

# ── ⑤ 版本 / tag 一致性 ────────────────────────────────────────────
if [ "$RELEASE" = "true" ]; then
  [ -n "$TAG" ] || bad "release 模式缺少 tag"
  if [ "v${VERSION}" = "$TAG" ]; then ok "版本与标签一致：${TAG}"; else bad "版本 ${VERSION} 与标签 ${TAG} 不一致"; fi
  [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || bad "版本号不合语义化：${VERSION}"
else
  [[ "$VERSION" =~ ^0\.0\.0-cl\..+ ]] || bad "CL 版本号应形如 0.0.0-cl.<run>-<sha>，实际 ${VERSION}"
  ok "CL 版本 ${VERSION}（本次不进入 release）"
fi

# ── ⑥ 签名 ─────────────────────────────────────────────────────────
if [ "$SIGNING" = "pem" ]; then
  if command -v openssl >/dev/null 2>&1; then ok "签名工具 openssl 就绪"; else bad "签名模式 pem 但缺少 openssl"; fi
  ok "将产出 SHA256SUMS.sig，并在 finalize 阶段回验"
elif [ "$SIGNING" = "none" ]; then
  hint "未配置签名密钥：仅生成 SHA256SUMS（未签名），不阻塞构建"
else
  bad "未知签名模式：${SIGNING}"
fi

# ── ⑦ 单平台过滤（六包校验仍全量执行）────────────────────────────────
SELECTED=("${EXPECTED[@]}")
if [ "$PLATFORM" != "all" ]; then
  hit=false
  for t in "${EXPECTED[@]}"; do [ "$t" = "$PLATFORM" ] && hit=true; done
  [ "$hit" = "true" ] || bad "平台 ${PLATFORM} 不在六包清单内"
  SELECTED=("$PLATFORM")
  echo "[plan] 单平台触发：只构建 ${PLATFORM}（上方六包校验仍全量执行）"
fi
for t in "${SELECTED[@]}"; do ok "将构建 ${t}"; done

if [ "$fail" -ne 0 ]; then
  echo "::error::plan 校验未通过"
  exit 1
fi

# ── 产出矩阵与 plan.json ───────────────────────────────────────────
mkdir -p "$ACTION_DIR"

export PLAN_VERSION="$VERSION"
export PLAN_RELEASE="$RELEASE"
export PLAN_TAG="$TAG"
export PLAN_SIGNING="$SIGNING"
export PLAN_PLATFORM="$PLATFORM"
export PLAN_SELECTED="$(printf '%s\n' "${SELECTED[@]}" | paste -sd, -)"

python3 - <<'PY' > "$ACTION_DIR/matrix.json"
import json, os
version = os.environ["PLAN_VERSION"]
runner = {"linux": "ubuntu-latest", "darwin": "macos-latest", "windows": "windows-latest"}
items = []
for t in os.environ["PLAN_SELECTED"].split(","):
    goos, goarch = t.split("/")
    items.append({
        "target": t,
        "goos": goos,
        "goarch": goarch,
        "runner": runner[goos],
        "package": f"artex-{version}-{goos}-{goarch}.zip",
    })
print(json.dumps(items, separators=(",", ":")))
PY

python3 - <<'PY' > "$ACTION_DIR/plan.json"
import json, os
version = os.environ["PLAN_VERSION"]
selected = set(os.environ["PLAN_SELECTED"].split(","))
six = [f"{o}/{a}" for o in ("linux", "darwin", "windows") for a in ("amd64", "arm64")]
print(json.dumps({
    "stage": "plan",
    "version": version,
    "tag": os.environ["PLAN_TAG"],
    "release": os.environ["PLAN_RELEASE"] == "true",
    "signing": os.environ["PLAN_SIGNING"],
    "platform_filter": os.environ["PLAN_PLATFORM"],
    "six_pack": [
        {"target": t, "selected": t in selected,
         "package": f"artex-{version}-{t.split('/')[0]}-{t.split('/')[1]}.zip"}
        for t in six
    ],
}, indent=2, ensure_ascii=False))
PY

matrix="$(cat "$ACTION_DIR/matrix.json")"
count="$(python3 -c 'import json,sys;print(len(json.load(open(sys.argv[1]))))' "$ACTION_DIR/matrix.json")"
{
  echo "matrix=${matrix}"
  echo "packages=${count}"
} >> "$GITHUB_OUTPUT"

echo "[plan] 通过：本次构建 ${count} 个包"
