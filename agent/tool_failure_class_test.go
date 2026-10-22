package agent

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/metrics"
	actool "github.com/Autumn-27/norma/tool"
)

// TestLooksLikeTransportFailure 固定「哪些失败算环境」的口径。
// 样本取自线上 artex.db 的真实报错文本（见 docs/诊断-2026-10-22）。
func TestLooksLikeTransportFailure(t *testing.T) {
	transport := []string{
		`Get "https://api.example.com/v1/x": EOF`,
		"curl: (35) schannel: failed to receive handshake, SSL/TLS connection failed",
		"Post \"https://a/b\": proxyconnect tcp: dial tcp 127.0.0.1:7897: connectex: No connection could be made",
		"dial tcp 1.2.3.4:443: connect: connection refused",
		"read tcp: connection reset by peer",
		`lookup foo.example: no such host`,
		"net/http: TLS handshake timeout",
		"x509: certificate signed by unknown authority",
		"代理不可用（127.0.0.1:7897 未监听）",
	}
	for _, s := range transport {
		if !looksLikeTransportFailure(s) {
			t.Fatalf("应判为 transport：%q", s)
		}
	}
	notTransport := []string{
		"",
		`参数校验未通过（stage=tool_validate，工具 Bash）：input: missing required property "command"`,
		`[exit code 1] bash: jq: command not found`,
		"HTTP 请求执行失败: 业务断言不成立（期望 status=200，实际 403）",
		"write tcp: 写入超出配额", // 工具自身的限制，不是链路问题
	}
	for _, s := range notTransport {
		if looksLikeTransportFailure(s) {
			t.Fatalf("不该判为 transport：%q", s)
		}
	}
}

// TestClassifyToolFailure 校验失败归档：超时/取消 → timeout，链路 → transport，其余 → exec。
func TestClassifyToolFailure(t *testing.T) {
	ctx := context.Background()

	if got := classifyToolFailure(actool.Errorf(`Get "https://a": EOF`), nil, ctx); got != metrics.StageTransport {
		t.Fatalf("EOF 应归 transport，实际 %s", got)
	}
	if got := classifyToolFailure(actool.Result{}, context.DeadlineExceeded, ctx); got != metrics.StageTimeout {
		t.Fatalf("DeadlineExceeded 应归 timeout，实际 %s", got)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got := classifyToolFailure(actool.Result{}, nil, canceled); got != metrics.StageTimeout {
		t.Fatalf("ctx 取消应归 timeout，实际 %s", got)
	}
	if got := classifyToolFailure(actool.Errorf("工具内部错误：index out of range"), nil, ctx); got != metrics.StageExec {
		t.Fatalf("普通失败应归 exec，实际 %s", got)
	}
	// err 非空但文本没特征时按 exec 计，不吞掉真实失败。
	if got := classifyToolFailure(actool.Result{}, errFake("something broke"), ctx); got != metrics.StageExec {
		t.Fatalf("无特征错误应归 exec，实际 %s", got)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
