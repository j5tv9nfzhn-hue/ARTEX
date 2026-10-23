package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/Autumn-27/artex/metrics"
	actool "github.com/Autumn-27/norma/tool"
)

// 本文件负责回答一个问题：这次工具失败，该记在谁头上？
//
// 实测（artex.db，131 条 is_error 工具结果）：
//   - 38 条是本地代理抖动导致的 TLS/EOF（`Get "https://…": EOF`、
//     `schannel: failed to receive handshake`）——环境问题，却把 http_request 的
//     错误率顶到 28.7%，让「工具错误率 11%」这个数完全失去行动含义；
//   - 17 条是超时/被取消；
//   - 真正可归因于平台或模型的（入参不合 schema、未知工具）只有 12 条。
//
// 所以把 transport（网络/代理层）单列，超时/取消也单独成档，剩下的才是工具自身失败。
// 这与 OpenTelemetry GenAI 语义约定的做法一致：工具 span 上用低基数 error.type 分类，
// 而不是一个布尔 is_error 盖住所有原因。

// transportFailureMarkers 是「请求没走到目标 / 链路在握手或读取中断」的文本特征。
// 只做指标归类，不改工具行为与返回值。
//
// 取舍：基于文本的判定必然有误判面（例如 Bash 里 grep 到含 "connection refused" 的
// 日志又恰好非零退出）。这批样本的代价只是统计口径，不影响任何执行路径，故接受；
// 一旦 SDK 侧能给出结构化错误类型，应改为按 error 类型判定。
var transportFailureMarkers = []string{
	": eof",                       // `Get "https://…": EOF`
	"unexpected eof",              // 响应体读到一半断链
	"failed to receive handshake", // schannel（Windows SChannel TLS）
	"schannel:",                   // curl.exe 经代理时的 SChannel 报错
	"proxyconnect",                // Go/curl 连不上代理
	"connection refused",          // 代理/目标未监听
	"connection reset by peer",    // 中间设备重置
	"no such host",                // DNS
	"i/o timeout",                 // 连接级超时（非工具预算超时）
	"tls handshake timeout",       // TLS 握手超时
	"x509: ",                      // 证书错误（含中间人证书未信任）
	"network is unreachable",
	"client.timeout exceeded", // http.Client.Timeout（工具自身的 ctx 超时另算）
	"代理不可用",                   // 我们自己的代理预检提示
}

// looksLikeTransportFailure 判断一段工具错误文本是否属于网络/代理层失败。
func looksLikeTransportFailure(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, marker := range transportFailureMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// classifyToolFailure 把一个失败的调用归类到 metrics 的阶段桶里。
//
//	timeout   —— ctx 超时/取消（步数/时长耗尽、用户叫停、任务暂停），非工具缺陷
//	transport —— 网络/代理层失败，非工具缺陷
//	exec      —— 其余：工具真的执行失败了（含幂等性未知的 HTTP 4xx/5xx 断言等）
func classifyToolFailure(res actool.Result, err error, callCtx context.Context) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		(callCtx != nil && (errors.Is(callCtx.Err(), context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.Canceled))) {
		return metrics.StageTimeout
	}
	text := ""
	if err != nil {
		text = err.Error()
	}
	if text == "" {
		text = res.Flatten()
	}
	if looksLikeTransportFailure(text) {
		return metrics.StageTransport
	}
	return metrics.StageExec
}
