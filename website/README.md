# ARTEX — 独立产品官网

独立静态多页面网站，统一维护于 ARTEX 总源码的 `website/` 子目录。**站点是纯静态 HTML / CSS / JS，无需任何构建步骤。不连接生产控制台，不运行分析引擎；与 `web/` 控制台前端分开维护和发布。**

## 源码位置与统一维护

- 当前目录：`C:\Users\Administrator\Desktop\ARTEX\website`
- 站点源码即部署内容：`site/` 目录下的静态 HTML / CSS / JS，直接编辑、直接预览、直接部署。
- 原独立目录 `Desktop\ARTEX-Website` 已整体迁入并改名为 `website`，不保留第二份活跃源码。
- `../web/` 是产品控制台；本目录是对外官网。
- 完整维护与发布入口见 [官网维护说明](../docs/website-maintenance.md)。
- 历史核对记录见 `deployment/` 与 `reports/`；`DELIVERY.json` 是部署前审核快照，不应当作当前源码校验。

## 最新部署状态（2026-10-17）

- 正式访问：https://artex-official-website.pages.dev/
- 本次最终部署快照：https://6fae4b04.artex-official-website.pages.dev/
- 用户已授权独立 Pages 项目发布。没有绑定自定义域名、修改 DNS、覆盖既有项目或部署 ARTEX 后端。
- 已在 Chromium 核查 10 个内容页与 404，深色桌面 / 浅色移动共 22 个组合；报告下载响应头问题已修复并实际下载复验。
- 官网性能分数仍为发布前本地测量，不代表线上网络表现。原交付 ZIP 是部署前审核快照，当前目录为发布后的源码。
- 2026-10-17 完成静态化改造：源码固化为纯静态 `site/`，不再需要构建；`site/` 29 个文件与线上发布哈希逐一核对一致，线上内容未变化。
- 2026-10-17 支持 `file://` 双击直开（相对路径 + 本地搜索索引）；该变更已随下方 v3 一并部署上线。
- 2026-10-17 完成视觉升级 v3（编辑式排版、双信号轨道信息图、层叠材质与滚动显影）并部署上线；同批上线显影优先级修复与全站 GitHub 仓库入口。发布记录见 `deployment/publish-design-upgrade.json`。

## 网站内容

| 路由 | 内容 |
|---|---|
| `/` | 品牌首页、双领域视角、交互式联合调查示例 |
| `/platform/` | 双领域产品结构、共享底座、能力状态矩阵 |
| `/platform/network/` | 网络安全对象、协作与授权范围 |
| `/platform/reverse/` | 样本、Provider、会话、函数和证据；能力筛选 |
| `/workflows/` | 先后、并行、版本化交付与人工检查点 |
| `/architecture/` | 领域模型、共享平台和规划中的项目层 |
| `/benchmarks/` | 本官网真实本地测量、条件与原始报告 |
| `/docs/` | 领域概念、接入原则、官网运行与发布准备 |
| `/updates/` | 历史源码交付记录及待验收事项 |
| `/trust/` | 安全、授权、数据与使用边界 |
| 未知路由 | 独立 404 页面，预览服务返回 HTTP 404 |

## 运行（纯静态，无需构建）

预览只使用 Node 标准库（`tools/serve.mjs`），无需安装依赖：

```sh
npm run preview
```

预览默认监听 `0.0.0.0:4173`，浏览器访问本机 `http://localhost:4173/`。可用环境变量 `PORT` 修改端口。页面与资源全部使用相对路径，也可以直接双击 `site/index.html` 打开（`file://`），不需要任何本地服务；搜索索引为本地数据文件 `site/assets/search.js`，离线同样可用。

只有运行浏览器检查或性能测量时才需要 `npm ci`（Playwright、axe、Lighthouse）。

### 静态化改造（2026-10-17）

- 移除 Node 构建器（原 `src/build.mjs`、`src/components.mjs`、`src/pages.mjs`）以及 `public/` + `dist/` 的产物式结构。
- 原 `dist/`（构建产物，即线上已发布内容）原样固化为源码 `site/`；本地预览服务迁移为 `tools/serve.mjs`。
- `site/` 中的 HTML 为压缩单行格式（与线上一致）；后续如做格式化美化，发布前必须重跑 `npm test` 复验布局与可访问性。
- canonical 与 sitemap 已固化 `https://artex-official-website.pages.dev/`；更换域名需全站替换 URL 并手工更新 `site/sitemap.xml`，不得虚构其他域名。

### 双击直开（file:// 支持，2026-10-17）

- 页面链接与资源改为相对路径（按页面层级），`site/index.html` 可直接双击打开，无需本地服务或依赖。
- 站内搜索索引由 `fetch` 的 `assets/search.json` 改为普通脚本 `assets/search.js`（`window.ARTEX_SEARCH`）；新增或修改页面后需手工同步该文件。
- 内页在 `<html>` 上声明 `data-root` 层级，供搜索结果的相对链接拼接；`404.html` 保留根路径（仅用于线上未知路由兜底，`file://` 下不适用）。
- 该变更已于 2026-10-17 随 v3 升级一并发布上线。转换记录见 `deployment/file-protocol-conversion.json`。

### 视觉升级 v3（2026-10-17）

- 站点样式全量重写为「仪器编辑风」：层叠大气层（网格 / 颗粒 / 光晕）、渐变墨大标题、页头幽灵字、悬停极光描边与指针聚光、滚动显影与跑马灯；首页 Hero 图像重构为内联 SVG「双信号轨道图」。
- 交互都在 `assets/site.js` 的追加模块中，渐进增强：显影依赖 `assets/theme.js` 预置的 `js-reveal` 类，并有 2.6 秒兜底；后续调整显影范围时，请同步修改 `site.css` 的 `js-reveal` 选择器组与 `site.js` 的 `revealList`。注意：隐藏规则与显示规则必须共用同一份 `:is(...)` 列表（显示规则写作 `:is(...).in`），否则列表中的复合选择器（如 `.page-hero>.eyebrow`）会把隐藏规则优先级抬高，内容拿到 `.in` 后仍停留在隐藏态（2026-10-17 曾出现并修复）。
- `format-strip` 的跑马灯由脚本克隆 `.marquee-track` 内容，不要手工复制第二份轨道；新增格式标签只需在原有轨道中加 `<span>`。
- 副页幽灵字来自各页 `<section class="page-hero container" data-fig="…">`，新增页面时补上该属性即可。
- 跨页 GitHub 入口：页头图标按钮（`.github-button`，复用 `.icon-button` 样式）与页脚仓库横幅卡片（`.footer-repo`，悬停极光描边 + 指针聚光已加入 `site.js` 聚光列表）；两处均新窗口打开公开仓库，仓库地址变更时需同步全部 11 个页面（每页 2 处）。
- 不新增资源文件；无内联脚本，CSP 不变；`file://` 双击直开与本地搜索索引不受影响。记录见 `deployment/design-upgrade-2026-10.json`。
- 该变更已于 2026-10-17 发布上线（快照与核查见 `deployment/publish-design-upgrade.json`）。

## 验证与测量（仅官网）

```sh
npx playwright install chromium
# Linux 如缺共享库，需按 Playwright 官方方式安装浏览器依赖。
npm test
npm run measure
```

先保持 `npm run preview` 运行。测试默认使用 `http://127.0.0.1:4173`；可用 `SITE_BASE` 指向本官网的另一个测试地址，**不要指向第三方目标**。

- `tests/browser.mjs`：内容路由与 404、1440/390 两种视口、深浅两种主题、axe 扫描、横向溢出、浏览器异常、内部链接和交互检查。
- `tests/measure.mjs`：Lighthouse 12.8.2 首页移动/桌面各一次，公开模拟节流配置和环境；生成 JSON/HTML 原始报告，并把可公开报告同步到 `site/reports/`。
- `reports/browser.json`：本地浏览器检查记录。
- `reports/measurements.json`：对外测量摘要。
- `reports/measured-manifest.json`：除测量页及报告外的站点文件哈希清单，用于绑定测量对象。
- `site/reports/`：可以通过网站下载的测量报告。

网站页面中的测量摘要与分数是静态文本；重新测量后如需对外更新，须同步修改 `site/benchmarks/index.html` 中引用的数值。

结果只代表记录中的 Chromium 与本地 Linux 沙箱条件，不代表 Windows 实机、Safari、Firefox、生产 SLA 或真实用户 Core Web Vitals。自动可访问性扫描不是完整合规认证。

没有对 ARTEX 主项目执行构建、编译、测试、迁移或引擎联调；历史源码交付数据在页面中单独标注。

## 目录

```text
site/                   纯静态站点源码（即部署内容，29 个文件）
  index.html 404.html … 各内容页目录（architecture/、platform/ 等）
  assets/               语义主题 CSS、交互 JS、本地字体、搜索索引
  reports/              公布的实测原始报告
  _headers robots.txt sitemap.xml routes.json
tools/serve.mjs         本地预览服务（Node 标准库、gzip、真实 404）
tests/                  浏览器检查与测量脚本
  browser.mjs
  measure.mjs
reports/                原始测量、截图、执行日志
deployment/             发布脚本与历史核对记录（deploy_once.py、deploy.log 等）
wrangler.toml           独立 Pages 配置，无账户、Token、正式域名
```

## Cloudflare / Wrangler 核查

通过用户指定的远程 MCP 检查：

- Wrangler 命令存在于用户 npm 全局目录，版本 **4.131.2**（现有 4.151.1 可用更新，非必须）。
- Node.js **v24.20.0**。
- 最初查询等待超时；后续带进程树清理与 10 秒上限的 `wrangler whoami` 成功返回，确认 CLI **已登录**。账号详情未输出、未写入网站；后续已验证可创建并部署本次独立项目。没有运行登录、注销，也未查看或导出 Token 值。
- ARTEX 主项目目录内 `**/wrangler*` 未匹配到配置。
- 首轮审核没有部署；后经明确授权创建并部署独立项目，**未绑定自定义域名、修改 DNS 或覆盖既有服务**。

当前配置已用于本次独立官网发布。更换账号、项目或绑定自定义域名须再次确认。`_headers` 为 Cloudflare Pages 准备，本地预览不应用 iframe 禁止策略，以兼容审核预览。生成的 Lighthouse HTML 报告使用内联脚本，因此以下载附件方式提供，保存后本地查看；内容页面脚本仅同源外置。

canonical 与 `site/sitemap.xml` 已固化当前正式域名。如绑定自定义域名，须全站替换 URL 并重新生成 sitemap；没有域名时不虚构 URL。

## 设计与资源

- v3「仪器编辑风」：编辑式大标题（酸柠→薄荷渐变墨）、层叠大气层（固定网格、颗粒、径向光晕）、轨道式内联 SVG 信息图、页头幽灵字、悬停极光描边与指针聚光、滚动显影与跑马灯；深浅两套语义 token 同步。
- 深墨绿 / 酸柠强调、暖灰浅色、全局语义 token。
- 自适应导航、原生 dialog 搜索、键盘操作与可见焦点。
- 支持系统主题、手动切换、偏好持久化、减少动态效果及暂停动效；显影系统在脚本失败时自动回退为静态内容。
- Manrope 本地 WOFF2，许可见 `site/assets/Manrope-LICENSE.txt`；中文使用系统字体。
- 技术叙事与可视化组织参考：[Awwwards / Shift 5](https://www.awwwards.com/sites/shift-5)，并参考 Awwwards / CSS Design Awards / FWA 2025–2026 获奖作品公开呈现的通用做法（大字排版、深色层叠材质、bento 网格、滚动叙事）。不复制这些项目的资产、截图或代码，不暗示 ARTEX 获奖。
- 无远程字体、CDN 脚本、统计跟踪和伪造客户 Logo。演示数据明确标注，不展示不存在的产品准确率或性能提升。

## 后续审核与改进

域名与账号、生产隐私说明、正式产品入口、跨浏览器与辅助技术人工检查、真实网络条件的性能复测。当前没有注册/支付/生产登录服务，因此不展示可误导用户的提交表单或试用按钮。
