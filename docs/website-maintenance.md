# 独立官网维护说明

## 源码入口

官网已从桌面独立目录 `ARTEX-Website` 整体迁入仓库根的 `website/`：

```text
ARTEX/
  web/       产品控制台前端（原有）
  website/   对外静态多页面官网（本次迁入）
  docs/website-maintenance.md
```

当前 Windows 路径：`C:\Users\Administrator\Desktop\ARTEX\website`。

官网是**纯静态站点，无需构建**：`website/site/` 下的 HTML / CSS / JS 即源码，也即部署内容。目录迁移或静态化不表示官网与控制台共享运行时：官网不连接真实任务引擎、账号或数据库；请勿把其内容复制到 `web/`、`server/webui/dist/`，也不要为官网运行仓库根的构建脚本。

## 日常维护

从总源码根进入 `website/`，只维护这个子项目。`node_modules/`、`.wrangler/` 和环境密钥已由该目录自己的 `.gitignore` 排除。

### PowerShell：预览官网（静态）

```powershell
Set-Location C:\Users\Administrator\Desktop\ARTEX\website
npm run preview          # 纯静态预览 site/，无需安装依赖
```

浏览器/可访问性检查（`npm test`）与性能测量（`npm run measure`）需要先 `npm ci`。不存在 `npm run build`；任何情况下不需要为官网执行构建。

## Cloudflare 发布

- 生产入口：https://artex-official-website.pages.dev/
- 项目名：`artex-official-website`，生产分支标签：`main`。
- `website/wrangler.toml` 是独立官网的 Pages 配置（`pages_build_output_dir = "./site"`）。
- `website/deployment/deploy_once.py` 根据脚本自身位置寻找官网根目录，直接上传 `site/`，不再执行构建。
- 修改官网后，须明确决定发布再执行上传；改源码本身不会更新线上网站。
- 已获发布授权时，在 `website/` 执行 `wrangler pages deploy site --project-name artex-official-website --branch main`。不要从 ARTEX 根目录直接运行该命令。
- 不要把 Token、`.wrangler/`、环境文件或账号信息提交到源码仓库。

## 迁移与历史记录

本次使用同卷目录重命名，并逐文件 SHA-256 核对原目录与迁入目录，包含隐藏文件和已有产物。随后仅更新官网 README、路径无关的部署脚本，以及总源码的维护入口。

`website/deployment/source-relocation.json` 记录迁移核对结果；`website/deployment/relocation-before-hashes.json`、`relocation-after-hashes.json` 记录对应文件哈希。

既有 `DELIVERY.json`、测量报告、发布日志和部署前审核 ZIP 继续保留为历史证据，不因迁移改写。官网性能分数仍为发布前本地测量，不是此次生产测量。

本次未构建或测试 ARTEX 主项目，未修改其前后端业务源码，未重新部署、改域名或 DNS。

### 静态化改造（2026-10-17）

将官网从「Node 生成器 + 构建」改为纯静态源码：

- 移除 `src/build.mjs`、`src/components.mjs`、`src/pages.mjs`，并将 `src/serve.mjs` 迁移为 `tools/serve.mjs`（仅本地预览）。
- 原 `dist/`（构建产物，即线上已发布内容）原样固化为源码 `site/`，29 个文件与线上发布哈希逐一核对一致；`public/` 内容并入 `site/` 后删除。
- 同步更新 `wrangler.toml`、`deployment/deploy_once.py`、`tests/browser.mjs`、`tests/measure.mjs`、`package.json`、`.gitignore`、官网 README 与本文档。
- 未重新部署，线上网站不变；未修改 ARTEX 主项目业务源码，未执行构建或测试。

### 双击直开（file:// 支持，2026-10-17）

- 官网页面链接与资源改为相对路径，`site/index.html` 可直接双击打开，无需本地服务或依赖；`npm run preview` 仍可用于预览与自动化检查。
- 搜索索引从 `assets/search.json`（fetch）改为 `assets/search.js`（`window.ARTEX_SEARCH`），供 `file://` 离线使用；新增页面后需手工同步该文件。
- `404.html` 仅用于线上未知路由兜底，保留根路径，`file://` 下不适用。
- 以上改动随后于 2026-10-17 随 v3 升级发布上线。记录见 `website/deployment/file-protocol-conversion.json`。

### 视觉升级 v3（2026-10-17）

- `site/assets/site.css` 全量重写为 v3「仪器编辑风」：语义 token 保留，新增固定网格 / 颗粒 / 光晕大气层、酸柠→薄荷渐变墨标题、页头幽灵字（`data-fig`）、悬停极光描边与指针聚光、滚动显影系统；响应式与打印规则同步重建。
- `site/index.html` Hero 替换为内联 SVG「双信号轨道图」（极网格、刻度环、双轨道、核心六边形、扫描扇区、流线、坐标标注）；`format-strip` 结构化为 `.marquee > .marquee-track`，由 `site.js` 克隆轨道内容实现无缝循环。
- `site/assets/site.js` 仅追加 v3 交互模块（页头滚动态、显影、聚光、计数、跑马灯、Hero 视差）；搜索、主题、工作流演示、复制等功能未改动。`site/assets/theme.js` 增加 `js-reveal` 预置与 2.6 秒兜底，脚本失败或减少动态效果时内容照常显示。
- 显影维护契约：隐藏规则 `html.js-reveal :is(...)` 与显示规则必须使用同一份 `:is(...)` 选择器列表（显示规则写作 `html.js-reveal :is(...).in`）；列表中的复合选择器（如 `.page-hero>.eyebrow`）会抬高隐藏规则优先级，若显示规则优先级不足，内容获得 `.in` 后仍停留在隐藏态（2026-10-17 修复过一次该缺陷）。调整显影范围时三处保持一致：`site.css` 选择器列表、`site.js` 的 `revealList`、`theme.js` 的 `js-reveal` 门控。
- 2026-10-17 增补 GitHub 仓库入口：页头图标按钮（`.github-button`）与页脚仓库横幅卡片（`.footer-repo`，悬停极光描边 + 指针聚光复用既有机制），跳转 `https://github.com/j5tv9nfzhn-hue/ARTEX`（新窗口）；链接硬编码于全部 11 个页面、每页 2 处，仓库地址迁移时需全量替换；组件纯静态，CSP 与 `file://` 双击直开不受影响。
- 不新增资源文件（仍 29 个）；无内联脚本，CSP 不变；`file://` 双击直开与 `window.ARTEX_SEARCH` 不受影响。
- 视觉方法参考 Awwwards / CSS Design Awards / FWA 2025–2026 获奖作品的公开通用做法；不复用其截图、资产或代码，也不暗示 ARTEX 获奖。
- 记录：`website/deployment/design-upgrade-2026-10.json`（before/after 哈希与改动清单）。已于 2026-10-17 发布上线：快照 `https://6fae4b04.artex-official-website.pages.dev/`，发布记录 `website/deployment/publish-design-upgrade.json`。
