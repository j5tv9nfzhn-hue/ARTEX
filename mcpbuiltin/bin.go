package mcpbuiltin

// artex-binscan：二进制静态分诊。纯 Go（debug/pe、debug/elf），不依赖 capa/yara/die。
// 目标不是替代专业工具，而是让模型在没有外部引擎的机器上也能"先看一眼"：
// 是什么格式、加没加壳、导入了什么、有没有可疑字符串与 IOC。

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Autumn-27/norma/mcp"
)

func binScanServer() Server {
	return Server{
		Name:    "artex-binscan",
		Title:   "ARTEX 二进制静态分诊（格式 / PE / 字符串 / 熵 / IOC）",
		Domain:  "reverse",
		Summary: "内置 Go 实现：文件类型与哈希、PE 结构/导入导出/节熵、ASCII 与 UTF-16 字符串、IOC 抽取。",
		Tools: []mcp.InProcessTool{
			{
				Name:        "file_identify",
				Description: "按魔数判断文件类型（PE/ELF/Mach-O/ZIP/APK/PDF/OLE/压缩包），给出大小、时间与 MD5/SHA256。",
				Schema: jsonSchema(map[string]any{
					"path": propStr("样本绝对路径或工作目录相对路径"),
				}, "path"),
				Handler: fileIdentify,
			},
			{
				Name:        "pe_info",
				Description: "解析 PE：架构、子系统、时间戳、入口点、节表与每节熵、导入库、导出符号。用于判断编译器/加壳与攻击面。",
				Schema: jsonSchema(map[string]any{
					"path":     propStr("PE 文件路径"),
					"max_imps": propInt("最多列出多少个导入符号，默认 60"),
				}, "path"),
				Handler: peInfo,
			},
			{
				Name:        "strings_scan",
				Description: "提取可打印字符串（ASCII + UTF-16LE）并给文件偏移，可按关键词过滤。用于快速找 URL、命令、注册表、错误信息。",
				Schema: jsonSchema(map[string]any{
					"path":    propStr("文件路径"),
					"min_len": propInt("最小长度，默认 4"),
					"grep":    propStr("可选子串过滤（大小写不敏感）"),
					"limit":   propInt("最多返回多少条，默认 400"),
					"utf16":   propBool("是否同时扫 UTF-16LE，默认 true"),
				}, "path"),
				Handler: stringsScan,
			},
			{
				Name:        "entropy_scan",
				Description: "计算整体与每节熵，结合节名给出加壳/加密线索（UPX、ASPack、Themida 等）。",
				Schema: jsonSchema(map[string]any{
					"path": propStr("文件路径"),
				}, "path"),
				Handler: entropyScan,
			},
			{
				Name:        "ioc_extract",
				Description: "从文件或给定文本里抽取 URL/域名/IP/邮箱/哈希/Windows 路径，去重后按类别输出。",
				Schema: jsonSchema(map[string]any{
					"path":     propStr("文件路径（与 text 二选一）"),
					"text":     propStr("直接给文本（与 path 二选一）"),
					"max_each": propInt("每类最多返回多少条，默认 40"),
				}),
				Handler: iocExtract,
			},
		},
	}
}

const maxAnalyzeBytes = 64 << 20

func readSample(path string) ([]byte, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return nil, fmt.Errorf("path 不能为空")
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("读不到文件：%w", err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s 是目录", p)
	}
	if st.Size() > maxAnalyzeBytes {
		return nil, fmt.Errorf("文件 %.1fMB 超过 %dMB 上限", float64(st.Size())/(1<<20), maxAnalyzeBytes>>20)
	}
	return os.ReadFile(p)
}

func fileIdentify(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	data, err := readSample(in.Path)
	if err != nil {
		return "", err
	}
	st, _ := os.Stat(in.Path)
	kind := detectKind(data)
	md5s := md5.Sum(data)
	sha := sha256.Sum256(data)
	var b strings.Builder
	fmt.Fprintf(&b, "文件：%s\n", in.Path)
	if st != nil {
		fmt.Fprintf(&b, "大小：%d 字节（%.2f MB）  修改时间：%s\n", st.Size(), float64(st.Size())/(1<<20), st.ModTime().Format("2006-01-02 15:04:05"))
	}
	fmt.Fprintf(&b, "类型：%s\n", kind)
	fmt.Fprintf(&b, "MD5：%s\nSHA256：%s\n", hex.EncodeToString(md5s[:]), hex.EncodeToString(sha[:]))
	fmt.Fprintf(&b, "熵：%.3f bits/byte\n", entropyOf(data))
	return b.String(), nil
}

func detectKind(d []byte) string {
	prefix := func(n int) []byte {
		if len(d) < n {
			return d
		}
		return d[:n]
	}
	switch {
	case len(d) >= 2 && d[0] == 'M' && d[1] == 'Z':
		return "PE（DOS MZ 头）"
	case len(d) >= 4 && string(d[:4]) == "\x7fELF":
		return "ELF"
	case len(d) >= 4 && (string(d[:4]) == "\xfe\xed\xfa\xce" || string(d[:4]) == "\xce\xfa\xed\xfe"):
		return "Mach-O (32)"
	case len(d) >= 4 && (string(d[:4]) == "\xfe\xed\xfa\xcf" || string(d[:4]) == "\xcf\xfa\xed\xfe"):
		return "Mach-O (64)"
	case len(d) >= 4 && string(d[:4]) == "PK\x03\x04":
		if len(d) > 36 && strings.Contains(string(prefix(36)), "AndroidManifest") {
			return "APK / JAR（ZIP）"
		}
		return "ZIP"
	case len(d) >= 5 && string(d[:5]) == "%PDF-":
		return "PDF"
	case len(d) >= 8 && string(d[:8]) == "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1":
		return "OLE / MS Office 旧格式"
	case len(d) >= 4 && string(d[:4]) == "\x1f\x8b\x08\x00":
		return "gzip"
	case len(d) >= 6 && string(d[:6]) == "7z\xbc\xaf\x27\x1c":
		return "7z"
	case len(d) >= 4 && string(d[:4]) == "Rar!":
		return "RAR"
	case len(d) >= 4 && string(d[:4]) == "\x89PNG":
		return "PNG"
	case len(d) >= 3 && d[0] == 0xff && d[1] == 0xd8 && d[2] == 0xff:
		return "JPEG"
	default:
		return "未知格式（魔数未匹配）"
	}
}

func entropyOf(d []byte) float64 {
	if len(d) == 0 {
		return 0
	}
	var freq [256]int
	for _, c := range d {
		freq[c]++
	}
	n := float64(len(d))
	var e float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		e -= p * math.Log2(p)
	}
	return e
}

func peInfo(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		MaxImps int    `json:"max_imps"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	data, err := readSample(in.Path)
	if err != nil {
		return "", err
	}
	if len(data) < 64 || data[0] != 'M' || data[1] != 'Z' {
		return "", fmt.Errorf("不是 PE 文件（缺少 MZ 头）")
	}
	f, err := pe.NewFile(strings.NewReader(string(data)))
	if err != nil {
		return "", fmt.Errorf("解析 PE 失败：%w", err)
	}
	defer f.Close()

	machine := map[uint16]string{0x14c: "x86", 0x8664: "x64", 0xaa64: "ARM64", 0x1c0: "ARM"}[f.Machine]
	if machine == "" {
		machine = fmt.Sprintf("0x%x", f.Machine)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "架构：%s  节数：%d  编译时间：%s\n", machine, f.NumberOfSections, time.Unix(int64(f.TimeDateStamp), 0).UTC().Format("2006-01-02 15:04:05 UTC"))
	subsystem, entry, imageBase := uint16(0), uint32(0), uint64(0)
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		subsystem, entry, imageBase = oh.Subsystem, oh.AddressOfEntryPoint, uint64(oh.ImageBase)
	case *pe.OptionalHeader64:
		subsystem, entry, imageBase = oh.Subsystem, oh.AddressOfEntryPoint, oh.ImageBase
	}
	subName := map[uint16]string{1: "NATIVE", 2: "GUI", 3: "CUI", 9: "Windows CE", 10: "EFI"}[subsystem]
	if subName == "" {
		subName = fmt.Sprintf("%d", subsystem)
	}
	fmt.Fprintf(&b, "子系统：%s  入口 RVA：0x%x  ImageBase：0x%x\n\n", subName, entry, imageBase)

	b.WriteString("节：\n")
	fmt.Fprintf(&b, "  %-10s %-10s %-10s %-8s %s\n", "名称", "VAddr", "RawSize", "熵", "备注")
	for _, s := range f.Sections {
		var ent float64
		if d, err := s.Data(); err == nil {
			ent = entropyOf(d)
		}
		note := ""
		switch strings.ToLower(strings.TrimSpace(s.Name)) {
		case "upx0", "upx1", "upx2":
			note = "UPX"
		case ".aspack", ".adata":
			note = "ASPack"
		case ".themida", ".winlicense":
			note = "Themida"
		}
		if ent > 7.2 && note == "" {
			note = "高熵（压缩/加密？）"
		}
		fmt.Fprintf(&b, "  %-10s 0x%-8x %-10d %-8.3f %s\n", s.Name, s.VirtualAddress, s.Size, ent, note)
	}

	libs, _ := f.ImportedLibraries()
	sort.Strings(libs)
	fmt.Fprintf(&b, "\n导入库（%d）：%s\n", len(libs), strings.Join(libs, ", "))
	syms, _ := f.ImportedSymbols()
	maxImps := clamp(in.MaxImps, 0, 500)
	if maxImps == 0 {
		maxImps = 60
	}
	if len(syms) > 0 {
		fmt.Fprintf(&b, "导入符号（%d，展示前 %d）：\n", len(syms), maxImps)
		for i, s := range syms {
			if i >= maxImps {
				b.WriteString("  …\n")
				break
			}
			fmt.Fprintf(&b, "  %s\n", s)
		}
	}
	b.WriteString("\n（导出表解析：COFF 符号可用时由 pe.Symbols 提供，此处暂不展开）\n")
	return b.String(), nil
}

func stringsScan(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Path   string `json:"path"`
		MinLen int    `json:"min_len"`
		Grep   string `json:"grep"`
		Limit  int    `json:"limit"`
		UTF16  *bool  `json:"utf16"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	data, err := readSample(in.Path)
	if err != nil {
		return "", err
	}
	minLen := defInt(in.MinLen, 4, 3, 32)
	if minLen == 0 {
		minLen = 4
	}
	limit := defInt(in.Limit, 400, 1, 5000)
	needle := strings.ToLower(strings.TrimSpace(in.Grep))
	utf16On := true
	if in.UTF16 != nil {
		utf16On = *in.UTF16
	}

	type item struct {
		Off int
		Enc string
		S   string
	}
	var items []item
	addIf := func(off int, enc, s string) bool {
		s = strings.TrimSpace(s)
		if len([]rune(s)) < minLen {
			return true
		}
		if needle != "" && !strings.Contains(strings.ToLower(s), needle) {
			return true
		}
		items = append(items, item{Off: off, Enc: enc, S: s})
		return len(items) < limit
	}

	cur := strings.Builder{}
	start := 0
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c >= 32 && c < 127 {
			if cur.Len() == 0 {
				start = i
			}
			cur.WriteByte(c)
			continue
		}
		if cur.Len() > 0 {
			if !addIf(start, "ascii", cur.String()) {
				cur.Reset()
				break
			}
			cur.Reset()
		}
	}
	if cur.Len() > 0 && len(items) < limit {
		addIf(start, "ascii", cur.String())
	}

	if utf16On && len(items) < limit {
		for i := 0; i+1 < len(data); i += 2 {
			if data[i+1] != 0 || data[i] < 32 || data[i] >= 127 {
				continue
			}
			j := i
			var runes []uint16
			for j+1 < len(data) && data[j+1] == 0 && data[j] >= 32 && data[j] < 127 {
				runes = append(runes, uint16(data[j]))
				j += 2
			}
			if len(runes) >= minLen {
				if !addIf(i, "utf16", string(utf16.Decode(runes))) {
					break
				}
			}
			i = j - 2
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s：命中 %d 条（min_len=%d，utf16=%v）\n\n", in.Path, len(items), minLen, utf16On)
	for _, it := range items {
		fmt.Fprintf(&b, "0x%08x [%s] %s\n", it.Off, it.Enc, clip(it.S, 300))
	}
	return b.String(), nil
}

func entropyScan(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	data, err := readSample(in.Path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s：整体熵 %.3f bits/byte（>7.2 通常意味着压缩或加密）\n\n", in.Path, entropyOf(data))

	f, err := pe.NewFile(strings.NewReader(string(data)))
	if err != nil {
		b.WriteString("非 PE，跳过节级分析。\n")
		return b.String(), nil
	}
	defer f.Close()
	fmt.Fprintf(&b, "%-12s %-10s %-8s %s\n", "节", "RawSize", "熵", "判断")
	for _, s := range f.Sections {
		d, err := s.Data()
		if err != nil {
			continue
		}
		e := entropyOf(d)
		verdict := "正常"
		switch {
		case e > 7.5:
			verdict = "极高（加密/压缩）"
		case e > 7.0:
			verdict = "偏高"
		case e < 3:
			verdict = "极低（大量填充）"
		}
		name := strings.ToLower(strings.TrimSpace(s.Name))
		for _, packer := range []string{"upx", "aspack", "mpress", "themida", "vmprotect", "enigma"} {
			if strings.Contains(name, packer) {
				verdict += " · 节名指向 " + strings.ToUpper(packer)
			}
		}
		fmt.Fprintf(&b, "%-12s %-10d %-8.3f %s\n", s.Name, s.Size, e, verdict)
	}
	return b.String(), nil
}

var (
	reURL     = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>)\]]{4,200}`)
	reEmail   = regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,24}\b`)
	reIPv4    = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`)
	reDomain  = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9\-]{0,61}[a-z0-9])?\.)+(?:com|net|org|cn|io|dev|app|info|xyz|top|ru|me|co|cc|tk|onion)\b`)
	reHash    = regexp.MustCompile(`\b(?:[a-f0-9]{64}|[a-f0-9]{40}|[a-f0-9]{32})\b`)
	reWinPath = regexp.MustCompile(`(?i)\b[a-z]:\\[^\s"'<>|*?]{3,200}`)
)

func iocExtract(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Text    string `json:"text"`
		MaxEach int    `json:"max_each"`
	}
	if err := decodeArgs(raw, &in); err != nil {
		return "", err
	}
	var body []byte
	switch {
	case strings.TrimSpace(in.Text) != "":
		body = []byte(in.Text)
	case strings.TrimSpace(in.Path) != "":
		d, err := readSample(in.Path)
		if err != nil {
			return "", err
		}
		body = d
	default:
		return "", fmt.Errorf("path 与 text 至少要给一个")
	}
	maxEach := defInt(in.MaxEach, 40, 1, 500)
	if maxEach == 0 {
		maxEach = 40
	}
	var b strings.Builder
	dump := func(title string, re *regexp.Regexp) {
		hits := dedupe(re.FindAllString(string(body), 5000))
		fmt.Fprintf(&b, "%s（%d）：\n", title, len(hits))
		if len(hits) == 0 {
			b.WriteString("  （无）\n")
			return
		}
		for i, h := range hits {
			if i >= maxEach {
				fmt.Fprintf(&b, "  …（还有 %d 条）\n", len(hits)-maxEach)
				break
			}
			fmt.Fprintf(&b, "  %s\n", clip(h, 200))
		}
	}
	dump("URL", reURL)
	dump("域名", reDomain)
	dump("IPv4", reIPv4)
	dump("邮箱", reEmail)
	dump("哈希", reHash)
	dump("Windows 路径", reWinPath)
	return b.String(), nil
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		k := strings.ToLower(strings.TrimSpace(s))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, strings.TrimSpace(s))
	}
	sort.Strings(out)
	return out
}
