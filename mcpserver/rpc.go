package mcpserver

import (
	"bytes"
	"encoding/json"
)

// JSON-RPC 2.0 信封与错误码。MCP 直接用这套信封，没有额外包装。

// Request 是一条入站 JSON-RPC 请求或通知（无 id 即通知）。
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response 是一条出站 JSON-RPC 响应。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error 是 JSON-RPC 错误体。
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error 让 Error 满足 error 接口，便于日志与测试断言。
func (e *Error) Error() string { return e.Message }

// JSON-RPC 2.0 预定义错误码。
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// hasID 判断一条请求是否带可用 id（带 id 才需要回响应）。
func hasID(id json.RawMessage) bool {
	t := bytes.TrimSpace(id)
	return len(t) > 0 && !bytes.Equal(t, []byte("null"))
}

// errorResponse 构造一条错误响应。
func errorResponse(id json.RawMessage, code int, msg string) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: msg}}
}
