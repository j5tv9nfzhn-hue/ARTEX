# 目标网站 https://trymodel.chat · 任务复盘手册 · 攻击/逆向突破链与脱敏证据案例库

> 本文档为技能包支撑文件（`references/attack-chains-and-pocs.md`），记录源报告中的结构化突破路径、关键漏洞点与已脱敏的 PoC 验证模板（仅供授权验证模式参考，严禁跨目标硬套凭据或域名）。

## 1. 完整推导链（Step-by-Step Progression）

1. 对 https://trymodel.chat 做前端静态深度测绘：抓取首页及全部 JS bundle/chunk 与 sourcemap，检索硬编码的上游模型供应商明文配置（base_url、api_url、openrouter.ai、api.openai.com、api.anthropic.com、api.deepseek.com 等域名，以及 sk-/Bearer/API_KEY/apiKey 等密钥字面量与 NEXT_PUBLIC_* 环境变量注入点），可调用 Skill(performing-api-inventory-and-discovery) 与 Skill(reverse-engineerin…（产出事实 1 条）
2. 对 https://trymodel.chat 做敏感配置与静态泄露探测：用 http_fuzz 枚举 /.env、/.env.local、/config.json、/api/config、/env.js、/_next/static 下 sourcemap、/git/、docker-compose.yml、vercel.json、备份与调试端点等，寻找上游供应商 URL 与 API KEY 明文/未授权配置接口（产出事实 1 条）
3. 用 browser MCP / Skill(playwright-cli) 真实渲染 https://trymodel.chat 并交互触发一次模型对话，捕获全部 XHR/fetch 与 WebSocket：记录前端实际调用的后端代理接口、请求/响应头、模型列表接口返回的 provider 元数据、以及错误信息中可能回显的上游供应商 URL 或密钥片段（产出事实 1 条、漏洞 1 条）
4. 对 https://trymodel.chat 的 AI 代理/模型接口做匿名与越权调用测试（Skill(performing-api-inventory-and-discovery)+Skill(performing-web-application-penetration-test)）：枚举 /api/chat、/api/v1/models、/api/generate 等端点，验证无鉴权可调用性；利用上游供应商 401/400 错误回显、响应头与 /models 端点推断上游明文 URL；尝试 model/provider/base_url 参数注入使其转发到可控地址以确认上游链路与密钥来源（产出事实 1 条、漏洞 1 条）
5. 对 https://trymodel.chat 管理面做鉴权矩阵与账号生命周期攻击：先探测 /api/auth/*（better-auth 特征：sign-up/sign-in/email、admin 插件端点、密码重置）能否自助注册/登录获取普通会话，再携带会话 Cookie 逐一请求已确认与推测的 /api/admin/*（config、config/custom、users、roles、models、settings、ai-sources/test 等），验证垂直/水平越权、HTTP 方法篡改、Cookie/Header 伪造与 admin.* 权…（产出事实 1 条）
6. 挖掘 https://trymodel.chat 的 TanStack Start 服务端函数面（TSS_SERVER_FN_BASE=/_serverFn/）：从 /assets/*.js 提取全部 functionId/编译标识与参数 schema，逐个用匿名与已登录会话安全调用并对比返回，重点寻找未做鉴权的配置读写、ai_sources 相关 server function（可能直接回显 baseUrl/apiKey），并测试 /_serverFn/ 500 错误回显与 functionId 路径注入；可调用 Skill(performing-api-inventory-and-discovery) 与 …
7. 溯源 https://trymodel.chat 所用开源/共用模板：bundle 残留 /src/content/pages/*.mdx 源码路径、admin.* 权限码与 https://hersoul.cn/api/payment/notify/alipay 支付回调，在 GitHub/代码托管检索同名或 her soul 系模板，定位 /api/admin/* 鉴权实现与 ai_sources 配置存储逻辑，提取默认凭证、未授权路由或已知漏洞用于定向绕过；可调用 Skill(performing-api-inventory-and-discovery) 与 tactical_kb(mode=skill_…
8. 枚举 trymodel.chat 的相邻基础设施与替代部署：用 DNS 枚举、证书透明度(CT)日志与被动 DNS 查 api./admin./staging./test./preview./dev. 等子域，并排查 *.vercel.app 预览部署、旧域名（含 her soul 系）中复用同一套 /api/config/public、/api/admin/config、/_serverFn/ 的前端实例，对比其鉴权强度；命中后用匿名/弱鉴权会话请求配置接口，验证是否回显 ai_sources 的 baseUrl+apiKey 明文（调用 Skill(…（产出事实 2 条、漏洞 1 条）
9. 通过历史归档与第三方缓存挖掘 trymodel.chat 旧版前端产物与配置泄露：用 Wayback Machine(web.archive.org)、urlscan.io 及公共 JS 缓存检索历史 bundle/chunk/sourcemap、旧 API 网关与旧配置端点，在产物中检索 sk-、Bearer、apiKey、baseUrl、api.sensenova.cn、api.openai.com 等字面量，并测试已下线但仍可访问的调试/配置路由（调用 Skill(reverse-engineering-malware-with-ghidra)、Skill(performing-api-inventory-and-discovery)）（产出事实 2 条）
10. 直接攻击上游网关 apipro.maynor1024.live（new-api / one-api 分支开源实例，assets.wlai.vip 静态资源，nginx，40.160.33.47）本体：枚举 /dashboard、/api/user、/api/token、/api/status、/api/log 等 new-api 管理面与令牌接口，测试默认/弱口令 admin 账号、已知 new-api CVE 与 token 伪造/会话机制、/api/redirect、/api/pricing、站内用户注册（若开放注册则注册后查看 /api/user/…

## 2. 已验证漏洞点与关键证据模板（共 0 项）

