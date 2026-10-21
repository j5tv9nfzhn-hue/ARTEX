package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxFrameBytes 是单帧上限。工具输出可能很大（strings_scan / http_send），
// 但 16 MiB 足够覆盖所有内置工具的截断上限，再大就是协议层异常。
const MaxFrameBytes = 16 << 20

// ServeStdio 在 in/out 上跑 MCP stdio 传输：一行一条 JSON-RPC 消息，
// 与 norma 的 stdio 客户端、以及 Codex / Claude Code / Cursor 的 stdio 模式一致。
//
// stdout 只写协议帧；调用方必须把日志导向 stderr，否则会把 JSON 流冲坏。
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	br := bufio.NewReaderSize(in, 64<<10)
	enc := json.NewEncoder(out)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		frame, err := readFrame(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		frame = bytes.TrimSpace(frame)
		if len(frame) == 0 {
			continue
		}
		resp := s.HandleFrame(ctx, frame)
		if resp == nil {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}

// readFrame 读一行（以 \n 结尾）。返回 io.EOF 表示流已结束。
func readFrame(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadBytes('\n')
	if len(line) > MaxFrameBytes {
		return nil, fmt.Errorf("frame exceeds %d bytes", MaxFrameBytes)
	}
	if err != nil {
		if len(bytes.TrimSpace(line)) > 0 && errors.Is(err, io.EOF) {
			return line, nil
		}
		return nil, err
	}
	return line, nil
}
