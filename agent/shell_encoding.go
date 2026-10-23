package agent

// 脚本编码分派（任务 26 步骤 ⑤）。
//
// 两条规则的方向是相反的，弄反任何一条都会坏：
//
//	.ps1        → UTF-8 **带 BOM** + CRLF
//	.bat/.cmd   → UTF-8 **不带 BOM** + CRLF
//
// .ps1 必须带 BOM：PowerShell 5.1 读脚本先看 BOM，没有 BOM 就按**系统 ANSI 代码页**
// 解码（中文 Windows = CP936/GBK）。UTF-8 的中文注释会被解成乱码，更糟的是 em dash
// 之类的字节序列可能被解成引号，让整段脚本解析失败（claude-code #90962）。
//
// .bat 必须不带 BOM：cmd.exe 会把 BOM 当成第一行的一部分，`@echo off` 变成
// `ï»¿@echo off`，第一条命令直接报「不是内部或外部命令」。这是最经典的翻车点。
//
// 两者都配 `chcp 65001`：让子进程按 UTF-8 读写控制台，避免中文输出变问号。

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// utf8BOM 是 UTF-8 BOM 字节序列。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// toCRLF 把 LF 归一成 CRLF（幂等）。Windows 的 cmd.exe 对 LF-only 的批处理有
// 已知的解析问题（goto/label 段），脚本一律用 CRLF。
func toCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// encodePowerShellScript 产出 .ps1 的字节：BOM + CRLF。
func encodePowerShellScript(body string) []byte {
	if !strings.HasPrefix(body, "\ufeff") {
		body = "\ufeff" + body
	}
	// 去掉可能存在的原始 BOM 字符，统一由字节层写一次。
	body = strings.TrimPrefix(body, "\ufeff")
	out := make([]byte, 0, len(utf8BOM)+len(body)+8)
	out = append(out, utf8BOM...)
	out = append(out, []byte(toCRLF(body))...)
	return out
}

// encodeBatchScript 产出 .bat/.cmd 的字节：**无** BOM + CRLF + chcp 65001。
func encodeBatchScript(body string) []byte {
	body = strings.TrimPrefix(body, "\ufeff")
	body = toCRLF(body)
	if !strings.Contains(strings.ToLower(body), "chcp 65001") {
		body = "chcp 65001>nul\r\n" + body
	}
	return []byte(body)
}

// writePowerShellScript 把脚本落盘为 5.1 / 7 都能正确解码的 .ps1。
func writePowerShellScript(path, body string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, encodePowerShellScript(body), 0o600)
}

// writeBatchScript 把脚本落盘为 cmd.exe 能直接执行的 .bat。
func writeBatchScript(path, body string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, encodeBatchScript(body), 0o600)
}

// decodeScriptFile 读回脚本并报告它的实际编码标记（诊断用；存量脚本只读诊断不改写）。
func decodeScriptFile(path string) (body string, bom bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	switch {
	case bytes.HasPrefix(raw, utf8BOM):
		return string(bytes.TrimPrefix(raw, utf8BOM)), true, nil
	case len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE:
		return string(raw), false, nil // UTF-16LE，交给上层 ensureUTF8Output
	default:
		return string(raw), false, nil
	}
}
