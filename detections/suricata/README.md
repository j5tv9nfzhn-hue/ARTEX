# ARTEX 检测规则（Suricata / 网络）


这是 [Sigma](../sigma/)的网络层配套，[Suricata](https://suricata.io)检测两类实际 UA。主机/日志层在 [sigma](../sigma/)，背景见指南[中文](../../docs/defense.md)/[English](../../docs/defense.md)。指标依据实际源码或下面明确说明的固定 SDK 例外，不是猜测。

## 规则：[`artex.rules`](artex.rules)

- **sid 1000001**：`ARTEX enrichment prober User-Agent`，UA 以 `artex-enrich/` 开头的入站 GET，来源 `enrich/enrich.go:233`，单请求存在指标，`classtype: attempted-recon`。
- **sid 1000002**：`ARTEX enrichment prober high-rate enumeration`，同 UA 的 `detection_filter` 超过**每来源 300 秒 30 请求**，表达自动化速度，对应 Sigma scan_velocity，`attempted-recon`。
- **sid 1000003**：`ARTEX worker WebFetch User-Agent`，UA 以 `norma/` 开头。SDK `github.com/Autumn-27/norma/tool/webfetch.go` 的原记录在 v0.1.0–v0.4.3 固定此 UA；记录代理不改请求头，因此到达目标。与补全不同，可在主动漏洞探测阶段观察，`attempted-recon`。

## 部署前须知：范围与局限

- **两类 UA 可见。** 侦察补全发 `artex-enrich/1.0`，WebFetch 发 `norma/0.4`；traffic 代理不改头。curl、nmap 等其他工具使用自身 UA，应结合常规扫描器签名和 [Sigma 行为规则](../sigma/)。
- **UA 只在可读取 HTTP 的位置可见。** 明文 HTTP 或终止 TLS 的代理/WAF 可检查，端到端 TLS 下不可直接读取。
- **静态 UA 可改变，缺失不证明安全。** 更持久的速度、广度信号由 sid 1000002 与 Sigma 关联表达，纯 Web 多阶段仍需环境基础规则。
- **刻意排除：** `artex-selfupdate` 通过 HTTPS 到 GitHub，普通 SNI 太常见；审计标记属于运营主机日志，使用 [guard Sigma](../sigma/artex_guard_audit_framing.yml)；服务/代理端口用于主机 ss/netstat 取证，不是网络签名。
- **固定依赖例外：** sid 1000003 的 `norma/` 来自 SDK，而非本仓库文件。go.mod 固定 v0.4.3、未 vendor；v0.1.0–v0.4.3 的硬编码是原分支记录，非本次重验。指标来源测试无法重读外部 SDK，故与 [CSV 说明](../indicators/README.md)一样明确这一例外，不把它冒称本地源码指标。

## 验证与测试

原资料使用 Suricata 8 验证；规则加载检查不需要实际流量。

```sh
# 语法与引擎加载，预期 Configuration provided was successfully loaded
docker run --rm -v "$PWD/detections/suricata":/r -w /r jasonish/suricata:latest \
  suricata -T -S artex.rules -l /tmp --init-errors-fatal
```

`--init-errors-fatal` 把初始化失败也视作错误，避免规则虽可解析却被静默丢弃、加载测试仍通过。

[Suricata 回归](../tests/suricata/)先执行加载检查，再用 scapy 确定性生成 pcap，运行 suricata -r 并断言告警数，仅需 Docker。

```sh
detections/tests/suricata/run.sh
```

存在规则在 35 流捕获中精确触发 35 次；速度规则超出 300 秒/30 请求，Suricata 8.0.7 基线为 5 次（第 31–35 流）；相同捕获使用正常浏览器 UA 时告警为零。详见[测试说明](../tests/README.md)。自有隔离环境也可捕获 loopback 的 `curl -A 'artex-enrich/1.0'`，不对未授权外部主机测试。

```sh
suricata -r enrich.pcap -S artex.rules -l out && \
  grep -c '"signature_id":1000001' out/eve.json    # 存在规则每探测一次
```

## 贡献

欢迎检测贡献。新规则必须有可观察依据，注释注明局限，通过 suricata -T，且不写成攻击指南。见[贡献指南](../../CONTRIBUTING.md)、[Sigma 层](../sigma/)及[总览](../README.md)。
