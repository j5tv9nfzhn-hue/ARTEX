package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// connectMCP 中文错误回归测试：stdio 无命令、http 无 URL、sse 无 URL、未知传输。
// 四条路径都在 dial 之前返回，可无数据库、无网络直接调用。
// 复用 assertChineseError，并保留传输枚举与 URL。
func TestConnectMCPErrorsLocalized(t *testing.T) {
	// 固定错误常量。
	for _, c := range []struct{ label, msg string }{
		{"stdio_no_command", errMCPStdioNoCommand},
		{"http_no_url", errMCPHTTPNoURL},
		{"sse_no_url", errMCPSSENoURL},
		{"unknown_transport", errMCPUnknownTransportFmt},
	} {
		assertChineseError(t, c.label, c.msg)
	}

	// 实际执行四个拒绝路径，均在 dial 前返回。
	for _, c := range []struct {
		label string
		srv   *db.MCPServer
	}{
		{"stdio_missing_command", &db.MCPServer{Transport: "stdio", Command: ""}},
		{"http_missing_url", &db.MCPServer{Transport: "http", URL: ""}},
		{"sse_missing_url", &db.MCPServer{Transport: "sse", URL: ""}},
		{"unknown_transport_path", &db.MCPServer{Transport: "websocket"}},
	} {
		_, err := connectMCP(context.Background(), c.srv)
		if err == nil {
			t.Fatalf("%s：未返回错误（验证通过）", c.label)
		}
		assertChineseError(t, c.label, err.Error())
	}
}
