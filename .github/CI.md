# CI 流水线（gate → plan → job → release）

本仓库的自动化 CL 构建由两个 workflow 组成：

| 文件 | 角色 |
| --- | --- |
| `.github/workflows/ci.yml` | 主流水线：gate → plan → job → release（+ 非强制 signals 探针） |
| `.github/workflows/cache-warm.yml` | 跨 workflow 缓存的**写入端**（在默认分支集中预热，供 ci.yml 只读消费） |

## 一、阶段职责

### 1. gate / 准入判定（`.github/scripts/gate.sh`）

不做任何构建，只把「这次构建什么、要不要发布」钉死，参数错误在这里就失败。

- **平台参数校验**：只接受 `all` 或六包中的一个（`linux/amd64`、`linux/arm64`、`darwin/amd64`、
  `darwin/arm64`、`windows/amd64`、`windows/arm64`）。
- **是否进入 release**：只有「显式 tag」才置真 —— `push` 到 `v*` 标签，或手动触发时填写 `tag`。
  PR 即使命中 tag 形态也强制不发布。**未提到 tag 时 `release=false`，流水线在 finalize 之后正常结束。**
- **版本号**：
  - release：`tag` 去掉 `v`（`v1.2.3` → `1.2.3`），要求 `vX.Y.Z[-suffix]` 形态；
  - CL 构建：`0.0.0-cl.<run_number>-<sha7>`，与任何发布版本都不会撞名。
- **标签存在性**：release 模式会用 `git ls-remote` 核对远端确有该 tag，避免 Release 挂在临时造出来的标签上。
- **签名能力**：`secrets.ARTEX_SIGNING_KEY` 存在 → `signing=pem`；否则 `signing=none`（仅校验和，不阻塞）。

### 2. plan / 三端六包校验（`.github/scripts/plan.sh`）

只读校验，不编译；**任何一项不通过即失败**，这是「能不能开工」的硬闸门。

1. **清单完整性**：必须是 linux / darwin / windows 三端 × amd64 / arm64 两架构，恰好 6 个包。
2. **工具链交叉核对**：每个目标都必须出现在 `go tool dist list` 里。
3. **每端两包**：`linux`、`darwin`、`windows` 各自恰好 2 个架构，防止矩阵被误改。
4. **标签/命名**：产物名必须匹配 `artex-<version>-<os>-<arch>.zip`，且能反解出正确的 os/arch；
   同时约定 windows 包内含 `artex.exe` + `start.bat`，其余含 `artex` + `start.sh`。
5. **版本一致性**：release 模式要求 `v<version> == tag` 且版本号语义化；CL 模式要求 `0.0.0-cl.*`。
6. **签名**：`pem` 模式要求 `openssl` 可用并承诺产出 `SHA256SUMS.sig`（finalize 会回验）；
   `none` 模式显式降级为「仅 SHA256SUMS」，不失败。
7. **单平台过滤**：`platform != all` 时只保留该目标，但**上面六项校验仍全量执行**。

产出：`ACTION/plan.json`（六包表 + 选中标记）与构建矩阵（矩阵只含选中的目标）。

### 3. job / 构建（三段）

| job | 职责 |
| --- | --- |
| `frontend` | `npm ci` + `npm run build:static`，产出 `web/out`，作为 `webui-dist` 构件供六个编译 job 复用（只构建一次前端） |
| `build`（矩阵 6 个） | 各平台原生 runner 上 `./build.sh --target <os/arch>`：内嵌前端、打包 zip 到 `ACTION/`，做归档完整性与可执行引信校验 |
| `finalize` | 汇总六个平台产物到 `ACTION/`，核对归档内容（二进制 / 启动脚本 / skills / 示例配置），生成统一 `SHA256SUMS`、`manifest.json`，有密钥时签名并回验签名 |

`signals` 是**非强制探针**（`go vet`、无需数据库的单元探针、二进制引信）：`continue-on-error: true`，
且不在任何 `needs` 链上 —— 它只提供信号，失败不会阻塞 `job` 与 `release`。

### 4. release / 发布

`if: needs.gate.outputs.release == 'true'`：仅当显式 tag 时执行，用 `gh release create --verify-tag`
把 `ACTION/` 里的 zip、`SHA256SUMS`、`manifest.json`（以及可选的 `SHA256SUMS.sig`）挂到该标签下。
**未给出 tag 时本 job 被跳过，整条流水线正常结束。**

## 二、缓存策略（跨 workflow）

依据 `actions/cache` 官方说明实现：缓存作用域是 **key + version + 分支**，且**默认分支上保存的缓存对其他分支可见**；
把 `restore` 与 `save` 拆成两个 action，即可做到「一处保存、多处只读复用」
（对应官方文档 *Make cache read only / Reuse cache from centralized job*）。

```
cache-warm.yml（写入端，只在 main / 定时 / 依赖变更时跑）
  ├─ warm-go  × { ubuntu, macos, windows }   → Go 模块缓存 + go build 构建缓存
  └─ warm-npm × { ubuntu }                   → npm 缓存
                    │  actions/cache/save
                    ▼
            （key: <os>-<arch>-artex-go-<hashFiles(go.sum)>-w<周>）
                    │  actions/cache/restore（只读）
                    ▼
ci.yml（消费端，六个编译 job + 前端 job）
```

- **key 组成**：`${runner.os}-${runner.arch}-artex-go-${hashFiles('go.sum')}-w${ISO周}`；
  npm 用 `hashFiles('web/package-lock.json')`。两个 workflow 的 key 完全一致，改动必须同步。
- **分层回退**：`restore-keys` 先按完整 hash 前缀匹配，再退到 `<os>-<arch>-artex-go-` 前缀，
  依赖没变时命中精确 key，依赖变了也能命中上一版做增量下载。
- **按 OS 分区**：缓存 version 含压缩方式与路径，Ubuntu 的缓存无法在 Windows 上复用，
  所以 cache-warm 对三端各预热一份；Go 的 `GOMODCACHE` / `GOCACHE` 用 `go env` 现取，避免硬编码路径。
- **不可变语义**：同一 key 保存一次后不再覆盖，所以 key 里带 ISO 周号 —— 每周可刷新一次，
  而 7 天未访问的缓存会被 GitHub 自动回收，周号正好与之对齐。
- **只读保护**：`ci.yml` 里 restore 一律用 `actions/cache/restore`（显式只读）；只有 **main 分支且未命中**时
  才用 `actions/cache/save` 回写，PR（fork 只读 token）不会反复尝试写缓存。
- **setup-go / setup-node 的内置缓存被显式关闭**（`cache: false` / `cache: ""`），
  避免与自定义 key 形成两套互不命中的缓存，让命中率可观测、可调。

## 三、触发方式

```bash
# 全平台 CL 构建（不发布）
.github/scripts/dispatch.sh

# 单平台 CL 构建
.github/scripts/dispatch.sh linux/amd64
.github/scripts/dispatch.sh windows/arm64

# 指定远端分支/提交
.github/scripts/dispatch.sh all "" feature/xyz

# 带 tag → 进入 release（tag 必须已 push 到远端）
.github/scripts/dispatch.sh all v1.2.3

# 列出可选平台
.github/scripts/dispatch.sh --list

# 当本地 origin 指向别的仓库（备份/镜像场景）时，显式指定目标仓库：
ARTEX_REPO=<owner>/ARTEX .github/scripts/dispatch.sh linux/amd64
```

等价的手动触发：

```bash
gh workflow run ci.yml -f platform=linux/amd64 -f tag= -f ref=
```

自动触发：`push` 到 `main`、`push` `v*` 标签、指向 `main` 的 PR。
push 标签 `v*` 会自动进入 release；其余一律是 CL 构建，流水线在 finalize 后正常结束。

## 四、产物路径

| 位置 | 内容 |
| --- | --- |
| 运行期工作目录 `ACTION/` | `artex-<version>-<os>-<arch>.zip` ×N、`SHA256SUMS`、`manifest.json`、`plan.json`、可选 `SHA256SUMS.sig` |
| Actions 构件 `artex-CL` | 即上面整个 `ACTION/` 目录（保留 14 天） |
| Actions 构件 `pkg-<os>-<arch>` | 单个平台的 zip（保留 7 天，便于只取一个平台） |
| GitHub Release | 仅在带 tag 时创建，附件为 `ACTION/` 中的 zip + `SHA256SUMS` + `manifest.json`（+ 签名） |

本地取回：

```bash
gh run download <run-id> -n artex-CL -D ACTION
sha256sum -c ACTION/SHA256SUMS
```

## 五、签名与校验

- 未配置密钥（默认）：只产出 `SHA256SUMS`，`manifest.json` 里 `"signing": "none"`。
- 配置 `ARTEX_SIGNING_KEY`（PEM 私钥）后：finalize 用 `openssl dgst -sha256 -sign` 生成
  `SHA256SUMS.sig`，**并立刻用导出的公钥回验**，回验失败即判失败。
  校验：`openssl dgst -sha256 -verify pub.pem -signature SHA256SUMS.sig SHA256SUMS`
