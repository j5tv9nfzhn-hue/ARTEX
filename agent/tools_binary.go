package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	actool "github.com/Autumn-27/norma/tool"
)

// binary_inspect is deliberately a bounded header/identity reader, not a
// disassembler. No child process, extraction, sample execution or database writes.
func (t *ToolSet) binaryInspectTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "binary_inspect",
		Description: "原生只读软件样本检查：流式 SHA-256、文件大小、PE/ELF/Mach-O/WASM/DEX/ZIP 魔数与基础头部、有限 ASCII 字符串及文件偏移。最多读取 512 MiB；字符串仅扫描前 4 MiB。不会执行样本或反编译；格式未知不代表安全。",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"file_path":   map[string]any{"type": "string", "description": "样本文件路径（相对当前工作目录或绝对路径）"},
			"max_strings": map[string]any{"type": "integer", "minimum": 0, "maximum": 100, "description": "返回 ASCII 字符串数量，0 不提取；默认 20，最多 100"},
		}, "required": []any{"file_path"}},
		ReadOnly: noStoreRo, Permissions: allowAllPerm,
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var a struct {
				FilePath   string `json:"file_path"`
				MaxStrings *int   `json:"max_strings"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if a.FilePath == "" {
				return actool.Errorf("file_path is required"), nil
			}
			n := 20
			if a.MaxStrings != nil {
				n = *a.MaxStrings
			}
			if n < 0 || n > 100 {
				return actool.Errorf("max_strings must be between 0 and 100"), nil
			}
			path := t.resolveLocalPath(a.FilePath)
			result, err := inspectBinaryFile(ctx, path, n)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(actool.Capture(tc, string(out))), nil
		},
	})
}

// InspectBinaryFile exposes bounded static inspection to the sample-library service.
func InspectBinaryFile(ctx context.Context, path string, maxStrings int) (map[string]any, error) {
	if maxStrings < 0 || maxStrings > 100 {
		return nil, fmt.Errorf("max_strings must be 0..100")
	}
	return inspectBinaryFile(ctx, path, maxStrings)
}

func inspectBinaryFile(ctx context.Context, path string, maxStrings int) (map[string]any, error) {
	// Reject ordinary device/FIFO paths before opening (also verify the opened handle below).
	preflight, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !preflight.Mode().IsRegular() {
		return nil, fmt.Errorf("sample must be a regular file")
	}
	if preflight.Size() > 512<<20 {
		return nil, fmt.Errorf("sample exceeds 512 MiB inspection limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("sample must be a regular file")
	}
	const maxSize int64 = 512 << 20
	if info.Size() > maxSize {
		return nil, fmt.Errorf("sample exceeds 512 MiB inspection limit; use a configured analysis provider")
	}
	// Keep memory and malformed-header parsing bounded independently of file claims.
	const scanLimit = 4 << 20
	prefix := make([]byte, 0, min(info.Size(), scanLimit))
	buf := make([]byte, 64<<10)
	hash := sha256.New()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		read, readErr := f.Read(buf)
		if read > 0 {
			total += int64(read)
			if total > maxSize {
				return nil, fmt.Errorf("sample grew beyond inspection limit")
			}
			_, _ = hash.Write(buf[:read])
			if len(prefix) < scanLimit {
				keep := min(read, scanLimit-len(prefix))
				prefix = append(prefix, buf[:keep]...)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if total != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, fmt.Errorf("sample changed during inspection; retry with a stable copy")
	}
	result := map[string]any{
		"file_path": path, "size_bytes": total, "sha256": hex.EncodeToString(hash.Sum(nil)),
		"format": "unknown", "executed": false, "decompiled": false,
		"identity_scope": "bytes read from this file handle; not an immutable stored sample",
		"limitations":    []string{"header-only format inspection; no function/type recovery", "ASCII strings only; sampled offsets are file_offset, not virtual addresses", "file stability checked by size and modification time, not an immutable snapshot"},
	}
	inspectBinaryHeader(prefix, result)
	type stringEntry struct {
		FileOffset string `json:"file_offset"`
		Text       string `json:"text"`
	}
	entries := make([]stringEntry, 0, maxStrings)
	truncated := false
	scanned := 0
	if maxStrings > 0 {
		for i := 0; i < len(prefix); {
			if i&65535 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			start := i
			for i < len(prefix) && prefix[i] >= 32 && prefix[i] <= 126 {
				i++
			}
			scanned = i
			if i-start >= 6 {
				if len(entries) >= maxStrings {
					truncated = true
					break
				}
				end := min(i, start+160)
				entries = append(entries, stringEntry{fmt.Sprintf("0x%x", start), string(prefix[start:end])})
				if end < i {
					truncated = true
				}
			}
			i++
			scanned = min(i, len(prefix))
		}
	}
	result["strings_requested"] = maxStrings > 0
	result["strings"] = entries
	result["strings_scan_bytes"] = func() int {
		if maxStrings == 0 {
			return 0
		}
		return scanned
	}()
	result["strings_truncated"] = maxStrings > 0 && (truncated || int64(scanned) < total)
	result["strings_scope"] = "first up to 4 MiB; ASCII runs of at least 6 bytes, each capped at 160 bytes; count bounded by max_strings"
	return result, nil
}

// Never trust section counts or offsets as allocation sizes.
func inspectBinaryHeader(b []byte, out map[string]any) {
	if len(b) >= 64 && b[0] == 'M' && b[1] == 'Z' {
		out["format"] = "DOS/MZ"
		off := uint64(binary.LittleEndian.Uint32(b[60:64]))
		if off > uint64(len(b)) || uint64(len(b))-off < 24 {
			out["header_status"] = "PE header outside inspected prefix or truncated"
			return
		}
		p := int(off)
		if !bytes.Equal(b[p:p+4], []byte{'P', 'E', 0, 0}) {
			return
		}
		out["format"] = "PE"
		out["machine"] = fmt.Sprintf("0x%04x", binary.LittleEndian.Uint16(b[p+4:p+6]))
		names := map[uint16]string{0x14c: "x86", 0x8664: "x86_64", 0xaa64: "arm64", 0x1c4: "armv7"}
		out["architecture"] = names[binary.LittleEndian.Uint16(b[p+4:p+6])]
		out["section_count"] = binary.LittleEndian.Uint16(b[p+6 : p+8])
		optSize := int(binary.LittleEndian.Uint16(b[p+20 : p+22]))
		opt := p + 24
		if optSize >= 20 && opt+optSize <= len(b) {
			magic := binary.LittleEndian.Uint16(b[opt : opt+2])
			out["optional_header_magic"] = fmt.Sprintf("0x%x", magic)
			out["entry_point_rva"] = fmt.Sprintf("0x%x", binary.LittleEndian.Uint32(b[opt+16:opt+20]))
			if magic == 0x10b {
				out["bits"] = 32
			}
			if magic == 0x20b {
				out["bits"] = 64
			}
			// CLR data directory is a hint, not proof that all code is managed.
			dir := 96
			numberOff := 92
			if magic == 0x20b {
				dir = 112
				numberOff = 108
			}
			if (magic == 0x10b || magic == 0x20b) && optSize >= dir+15*8 && binary.LittleEndian.Uint32(b[opt+numberOff:opt+numberOff+4]) >= 15 {
				out["clr_directory_present"] = binary.LittleEndian.Uint32(b[opt+dir+14*8:opt+dir+14*8+4]) != 0
			}
		} else {
			out["header_status"] = "optional header truncated"
		}
		return
	}
	if len(b) >= 20 && bytes.Equal(b[:4], []byte{0x7f, 'E', 'L', 'F'}) {
		out["format"] = "ELF"
		class := b[4]
		if class != 1 && class != 2 {
			out["header_status"] = "invalid ELF class"
			return
		}
		var order binary.ByteOrder = binary.LittleEndian
		if b[5] == 2 {
			order = binary.BigEndian
			out["endianness"] = "big"
		} else if b[5] == 1 {
			out["endianness"] = "little"
		} else {
			out["header_status"] = "invalid ELF endianness"
			return
		}
		out["bits"] = int(class) * 32
		out["machine"] = fmt.Sprintf("0x%x", order.Uint16(b[18:20]))
		arch := map[uint16]string{3: "x86", 62: "x86_64", 40: "arm", 183: "arm64", 243: "riscv"}
		out["architecture"] = arch[order.Uint16(b[18:20])]
		if class == 1 && len(b) >= 52 {
			out["entry_point_va"] = fmt.Sprintf("0x%x", order.Uint32(b[24:28]))
		} else if class == 2 && len(b) >= 64 {
			out["entry_point_va"] = fmt.Sprintf("0x%x", order.Uint64(b[24:32]))
		} else {
			out["header_status"] = "truncated ELF header"
		}
		return
	}
	if len(b) >= 4 {
		magic := binary.BigEndian.Uint32(b[:4])
		switch magic {
		case 0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe:
			out["format"] = "Mach-O"
			out["bits"] = 32
			if magic == 0xfeedfacf || magic == 0xcffaedfe {
				out["bits"] = 64
			}
			var order binary.ByteOrder = binary.BigEndian
			if magic == 0xcefaedfe || magic == 0xcffaedfe {
				order = binary.LittleEndian
			}
			if len(b) >= 28 {
				out["cpu_type"] = fmt.Sprintf("0x%x", order.Uint32(b[4:8]))
				out["load_command_count"] = order.Uint32(b[16:20])
			} else {
				out["header_status"] = "truncated Mach-O header"
			}
			return
		case 0xcafebabe, 0xbebafeca, 0xcafebabf, 0xbfbafeca:
			out["format"] = "fat-magic container (Mach-O universal or Java class)"
			out["header_status"] = "magic match only; requires provider validation"
			return
		}
	}
	if len(b) >= 8 && bytes.Equal(b[:4], []byte{0, 'a', 's', 'm'}) {
		out["format"] = "WASM"
		out["version"] = binary.LittleEndian.Uint32(b[4:8])
		return
	}
	if len(b) >= 8 && bytes.Equal(b[:4], []byte{'d', 'e', 'x', '\n'}) {
		out["format"] = "DEX"
		out["version"] = string(b[4:7])
		return
	}
	if len(b) >= 4 && (bytes.Equal(b[:4], []byte{'P', 'K', 3, 4}) || bytes.Equal(b[:4], []byte{'P', 'K', 5, 6})) {
		out["format"] = "ZIP container"
		out["header_status"] = "container only; APK/JAR classification requires inspecting entries"
	}
}
