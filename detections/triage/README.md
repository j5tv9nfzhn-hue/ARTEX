# ARTEX 主机研判（triage）


[`artex_host_triage.py`](artex_host_triage.py)直接在疑似运行过 ARTEX 的**单台主机**执行只读研判。其他目录面向 [SIEM](../sigma/)、[网络传感器](../suricata/)和[情报平台](../indicators/)；本脚本面向无 SIEM、需仅凭本地状态快速且有依据地判断“这里是否运行过 ARTEX”的响应人员。

检查已有指纹，并覆盖[清单](../indicators/artex_indicators.csv)故意不设 Sigma 规则、只能在主机/数据库观察的三项：server-listen-port、recording-proxy-endpoint、postgres-exploration-schema。

## 检查内容

所有检查基于本仓库已确认的字符串/路径，每个发现注明与对应 Sigma 规则或指标行相同的局限。

- **监听端口：** :8787 管理 UI 与 127.0.0.1:8788 记录代理，来自 [main.go](../../cmd/artex/main.go)的 --addr/--proxy 默认值。解析 ss/netstat/lsof，或读取 --ports-from 文件。
- **记录代理产物：** 首次运行生成 `<数据目录>/traffic/_ca/mitmproxy-ca-cert.pem`，以及 `_index/index.sqlite`、`_blobs/`，来源 [traffic.go](../../traffic/traffic.go)。数据默认在可执行文件旁 data/；该 MITM CA 是 HTTP(S) 解密记录器的信任锚（T1557）。
- **日志标记：** 查找 [补全 UA](../../enrich/enrich.go) artex-enrich/1.0、[更新 UA](../../selfupdate/github.go) artex-selfupdate、[guard 审计标记](../../guard/guard.go)，保留非 ASCII framing 以真实匹配。支持 .gz/.bz2/.xz 历史日志；标准库无编解码器的 .zst/.lz4 明确报告跳过，不当作干净，需解压或手工检查。
- **PostgreSQL 探索结构：** 检查 [schema_sqlite.sql](../../db/schema_sqlite.sql)中的 exploration_nodes/_edges/_anchors、assets/companies/activity 与 agent_prompts 种子（上游 PostgreSQL 版定义在 `db/schema.sql`）。提供 DSN 时用 psql 查询，缺少 psql 则打印可手工执行的只读 SQL。
- **进程环境注入：** 同时带 HTTP_PROXY/HTTPS_PROXY/ALL_PROXY 和指向 mitmproxy-ca-cert.pem 的 SSL_CERT_FILE/CURL_CA_BUNDLE/REQUESTS_CA_BUNDLE/GIT_SSL_CAINFO/NODE_EXTRA_CA_CERTS，源于 [worker.go](../../agent/worker.go) proxyEnv 与[断言](../../agent/proxyenv_test.go)。名字固定，仅值可改，较端口更特异，改名/改端口仍可能保留。Linux 读取 /proc，离线读 --proc-from。代理与 CA 同在为高严重度；单独 mitmproxy CA 或默认 127.0.0.1:8788 为中等；无该 CA 的公司代理不作为线索。

**发现是研判线索，不是定论；无发现也不代表安全。** 运营者可改二进制名、数据目录或端口。

## 平台支持

纯 Python 3 标准库，原则上适用于支持 Python 3 的平台；项目原有 Linux CI 自测与 macOS 执行记录。两项 OS 相关检查均明确降级，不把不可用当作安全。

- **实时端口：** 依次 ss → netstat → lsof，使用第一个有输出的工具。macOS/BSD 无 ss、netstat 不接受 Linux -ltnp 时转用 `lsof -nP -iTCP -sTCP:LISTEN`；离线用 --ports-from。
- **实时进程环境：** /proc 仅 Linux 可用；macOS/BSD 报告跳过而不是干净，可在 Linux 捕获后通过 --proc-from 导入。

其他检查依赖文件系统、日志与可选 DSN 下的 psql，无特定 OS 依赖。

## 使用方法

```sh
# 检查主机各类本地线索
detections/triage/artex_host_triage.py \
    --data-dir /opt/artex/data \
    --log /var/log/syslog --log-dir /var/log/artex \
    --pg-dsn "$ARTEX_PG_DSN"

# 输出机器可读格式；出现任何指标则返回非零
detections/triage/artex_host_triage.py --data-dir /opt/artex/data --json --exit-code

# 离线/取证镜像：读取捕获的进程环境转储
#   在原主机生成转储：
#   for p in /proc/[0-9]*; do echo "# $p"; tr '\0' '\n' < "$p/environ"; echo; done > proc_env_dump.txt
detections/triage/artex_host_triage.py --proc-from proc_env_dump.txt

# 可复现 fixture 自测，不读取或改变真实主机状态
detections/triage/artex_host_triage.py --self-test
```

无安装、无网络，仅 self-test 写入自己的临时目录。正常执行读取端口、数据目录、日志及仅在提供 DSN 时读取数据库，输出发现。默认退出 0，因用途为研判而非门禁；--exit-code 在发现任何指标时退出 1。

## 如何保持验证可靠

--self-test 创建合成主机：CA、索引、blob 数据目录，各标记日志、端口表及环境转储。断言所有检测触发，再断言干净主机、正常日志、公司代理零发现。自测接入 [CI](../../.github/workflows/detections.yml)与 [run-all](../tests/run-all.sh)，检查失效或指标偏离源码时应阻止合并，不以不可复现主张代替验证。
