package server

import (
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// Go archive/zip 仅内置 Store(0)、Deflate(8)，遇到其他算法返回 unsupported compression algorithm。
// 7-Zip、WinZip 可能使用 bzip2 或 zstd，在此补充两个纯 Go 解码器。
// Deflate64、LZMA、XZ、PPMd 和加密文件仍不支持，解压前给出中文提示，
// 而非直接向用户暴露底层解码错误。
const (
	zipMethodStore     = 0
	zipMethodDeflate   = 8
	zipMethodDeflate64 = 9
	zipMethodBzip2     = 12
	zipMethodLZMA      = 14
	zipMethodZstdPKW   = 20 // PKWARE 早期分配给 zstd 的方法编号。
	zipMethodZstd      = 93
	zipMethodXZ        = 95
	zipMethodJPEG      = 96
	zipMethodWavPack   = 97
	zipMethodPPMd      = 98
	zipMethodAES       = 99
)

var zipMethodNames = map[uint16]string{
	zipMethodStore:     "Store",
	zipMethodDeflate:   "Deflate",
	zipMethodDeflate64: "Deflate64",
	zipMethodBzip2:     "bzip2",
	zipMethodLZMA:      "LZMA",
	zipMethodZstdPKW:   "Zstandard",
	zipMethodZstd:      "Zstandard",
	zipMethodXZ:        "XZ",
	zipMethodJPEG:      "JPEG",
	zipMethodWavPack:   "WavPack",
	zipMethodPPMd:      "PPMd",
	zipMethodAES:       "AES 加密",
}

// Skill 上传错误由 fsUploadSkill 以 writeErr(400, err.Error()) 直接返回。
const (
	errSkillZipParse       = "无法解析压缩文件（必须为 zip 格式）：%w"
	errSkillZipEncrypted   = "压缩文件已加密（%s）。请上传未加密的 zip 文件。"
	errSkillZipUnsupported = "不支持的压缩方式：%s (method %d)，文件 %s。 " +
		"请使用“存储 (Store)”或“Deflate”方式重新压缩" +
		"（7-Zip、WinRAR 中请选择 Deflate，也可使用操作系统自带压缩功能或命令行 zip -r）。"
)

func zipMethodName(m uint16) string {
	if n, ok := zipMethodNames[m]; ok {
		return n
	}
	return "未知"
}

// newSkillZipReader parses an uploaded archive and registers the extra decompressors
// we can support beyond the stdlib's Store/Deflate.
func newSkillZipReader(buf []byte) (*zip.Reader, error) {
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return nil, fmt.Errorf(errSkillZipParse, err)
	}
	zr.RegisterDecompressor(zipMethodBzip2, func(r io.Reader) io.ReadCloser {
		return io.NopCloser(bzip2.NewReader(r))
	})
	zdec := zstd.ZipDecompressor(zstd.WithDecoderConcurrency(1))
	zr.RegisterDecompressor(zipMethodZstd, zdec)
	zr.RegisterDecompressor(zipMethodZstdPKW, zdec)
	return zr, nil
}

// skillZipEntry pairs a zip entry with its decoded (UTF-8) name — f.Name may hold
// raw GBK bytes, see zipEntryName.
type skillZipEntry struct {
	f    *zip.File
	name string
}

// skillZipEntries lists the archive's real files (no directory entries, no archiver
// junk) with their names decoded to UTF-8.
func skillZipEntries(zr *zip.Reader) []skillZipEntry {
	out := make([]skillZipEntry, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := zipEntryName(f)
		if strings.HasPrefix(name, "__MACOSX/") || strings.Contains(name, "/__MACOSX/") ||
			path.Base(name) == ".DS_Store" {
			continue // macOS 打包时产生的辅助条目。
		}
		out = append(out, skillZipEntry{f: f, name: name})
	}
	return out
}

// zipEntryName 返回 UTF-8 路径。Windows 的部分压缩工具未设置 UTF-8 flag 时，
// 可能以 GBK 存储非 ASCII 文件名，Go 会保留原始字节；
// 为避免 UTF-8 和路径校验失败，在此尝试 GBK 解码。
func zipEntryName(f *zip.File) string {
	if utf8.ValidString(f.Name) {
		return f.Name
	}
	if dec, err := simplifiedchinese.GBK.NewDecoder().String(f.Name); err == nil && utf8.ValidString(dec) {
		return dec
	}
	return f.Name
}

// checkSkillZipMethods rejects archives we cannot extract, naming the offending
// entry and method instead of letting f.Open() fail with an opaque English error.
func checkSkillZipMethods(entries []skillZipEntry) error {
	for _, e := range entries {
		if e.f.Flags&0x1 != 0 || e.f.Method == zipMethodAES {
			return fmt.Errorf(trMsg(errSkillZipEncrypted), e.name)
		}
		switch e.f.Method {
		case zipMethodStore, zipMethodDeflate, zipMethodBzip2, zipMethodZstd, zipMethodZstdPKW:
		default:
			return fmt.Errorf(errSkillZipUnsupported,
				zipMethodName(e.f.Method), e.f.Method, e.name)
		}
	}
	return nil
}
