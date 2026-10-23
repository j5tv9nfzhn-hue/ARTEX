# AGENTS.md

本仓库对 AI 助手 / 自动化代理的**硬性约定**。完整版见 [`README.md` 的「开发与发布约定」](README.md#开发与发布约定长期生效--最高优先级)，冲突时以该节为准。

## 一、本机只读 + 只写，不构建

在用户本机的工作副本上，**禁止**执行任何测试、编译、构建、打包命令：

```text
go build / go vet / go test / go run / gofmt / golangci-lint
npm ci|install|test|run build|run build:static、npx tsc、npx biome、next build
build.sh、dev.sh、任何打包脚本
```

含间接方式：MCP shell 转发、写 `.bat`/`.ps1` 去跑、`docker run` 跑等价命令。

**允许**：读文件、写文件、列目录、`git status|diff|log|show`、`git add|commit|push|tag`、`gh` 只读查询。

构建与验证一律由 GitHub Actions 承担（`release.yml` 构建与发布，`detections.yml` 检测规则）。

## 二、门禁分层

- **编译 / 静态检查失败 = 必须拦住**，不得出包（`go-build` job）。
- **测试失败 = 只是健康度信号**，不得阻塞打包与发版（`go-test` job 独立运行）。
- **发布流程失败 = 必须失败并清理**，不留半成品（`release.yml` 失败即删草稿、保 tag）。

"测试不拦"不等于"什么都不拦"：编译类确定性失败必须拦死。

## 三、网络必须走代理 7897

裸直连 GitHub 会稳定超时（~20 秒）。一律：

```bash
git -c http.proxy=http://127.0.0.1:7897 -c https.proxy=http://127.0.0.1:7897 <子命令>
```

`gh` 用 `HTTP_PROXY` / `HTTPS_PROXY` 环境变量。按次传参，不写全局 `git config`。
单次 20 秒无响应即判不可达，最多重试 2 次；仍失败就停止并报告，不裸连硬刷。

## 四、推送即返回

不轮询 CI、不盯 Actions 日志、不为确认成功反复查询。唯一串行要求：**tag 必须在新 commit 已到远端之后创建**。

## 五、静态自检代替编译

写完回读改动文件，核对 import、符号名、类型签名、调用点参数、结构体字段、i18n 键（`web/messages/zh.json`）。
不凭记忆写符号名，不把"让 CI 去发现"当作检查手段。

## 六、版本与发布

- 每版必发 GitHub Release，tag 与 Release 同名。
- 版本删除后重发沿用同一版本号，不递增。
- 更新源白名单只有 `j5tv9nfzhn-hue/ARTEX`，保留白名单 + SHA256 双闸门。
- 自动更新（安装）默认关，自动检测默认开，两者都有开关。

## 七、提交

一次语义完整的改动合成单个提交，中文提交信息，说明"改了什么 / 为什么"。
不提交构建产物（`dist/`、`server/webui/dist/`、`web/out/`）、不提交密钥与本地配置。
