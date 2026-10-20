package binlab

import (
	"fmt"
	"strings"
)

// Indicator is one triage observation. Severity is info, low, medium or high.
type Indicator struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

// Audit is the triage view of an image: what kind of thing this is, whether
// static reasoning can be trusted, and which observations drove that call.
type Audit struct {
	Path                string      `json:"path"`
	SHA256              string      `json:"sha256"`
	Format              string      `json:"format"`
	Arch                string      `json:"arch"`
	Bits                int         `json:"bits"`
	AddressKind         string      `json:"address_kind"`
	EntryRVA            string      `json:"entry_rva"`
	ImageBase           string      `json:"image_base"`
	PackerScore         int         `json:"packer_score"`
	Verdict             string      `json:"verdict"`
	Indicators          []Indicator `json:"indicators"`
	HighEntropySections []string    `json:"high_entropy_sections"`
	RWXSections         []string    `json:"rwx_sections"`
	ImportSymbolCount   int         `json:"import_symbol_count"`
	ImportLibraries     []string    `json:"import_libraries"`
	ExportCount         int         `json:"export_count"`
	OverlayBytes        int64       `json:"overlay_bytes"`
	Crypto              []string    `json:"crypto"`
	StopLines           []string    `json:"stop_lines"`
	NextSteps           []string    `json:"next_steps"`
	Limitations         []string    `json:"limitations"`
}

// packerSections maps a known packer/protector section name to its family.
var packerSections = map[string]string{
	"UPX0":     "UPX",
	"UPX1":     "UPX",
	"UPX2":     "UPX",
	".ASPACK":  "ASPack",
	".ADATA":   "ASPack",
	".THEMIDA": "Themida",
	".WINLICE": "Themida / WinLicense",
	".VMP0":    "VMProtect",
	".VMP1":    "VMProtect",
	".VMP2":    "VMProtect",
	".NSP0":    "NsPack",
	".PETITE":  "Petite",
	".MPRESS1": "MPRESS",
	".MPRESS2": "MPRESS",
	".ENIGMA1": "Enigma Protector",
	".ENIGMA2": "Enigma Protector",
	".PERPLEX": "Perplex",
	".BOOM":    "The Boomerang",
}

// antiDebugAPIs are the imports that most often appear in a debugger check.
var antiDebugAPIs = []string{
	"IsDebuggerPresent",
	"CheckRemoteDebuggerPresent",
	"NtQueryInformationProcess",
	"NtSetInformationThread",
	"NtQueryObject",
	"OutputDebugStringA",
	"OutputDebugStringW",
	"GetTickCount",
	"GetTickCount64",
	"QueryPerformanceCounter",
	"SetUnhandledExceptionFilter",
	"CreateToolhelp32Snapshot",
	"Process32First",
	"Process32Next",
	"FindWindowA",
	"FindWindowW",
	"GetForegroundWindow",
	"BlockInput",
}

// injectionAPIs signal runtime code generation or cross-process writes.
var injectionAPIs = []string{
	"VirtualAllocEx",
	"VirtualProtectEx",
	"WriteProcessMemory",
	"CreateRemoteThread",
	"NtUnmapViewOfSection",
	"SetThreadContext",
	"QueueUserAPC",
	"MapViewOfFile",
	"NtCreateSection",
}

// dynamicResolveAPIs mean the import table cannot be trusted as the API list.
var dynamicResolveAPIs = []string{
	"LoadLibraryA",
	"LoadLibraryW",
	"LoadLibraryExA",
	"LoadLibraryExW",
	"GetProcAddress",
	"LdrGetProcedureAddress",
}

// Analyze produces the triage view. It is pure: no execution, no attachment.
func (img *Image) Analyze() *Audit {
	a := &Audit{
		Path:         img.Path,
		SHA256:       img.SHA256,
		Format:       img.Format,
		Arch:         img.Arch,
		Bits:         img.Bits,
		AddressKind:  img.AddressKind,
		EntryRVA:     fmt.Sprintf("0x%x", img.EntryRVA),
		ImageBase:    fmt.Sprintf("0x%x", img.ImageBase),
		OverlayBytes: img.OverlaySize,
	}
	score := 0
	add := func(id, severity, title, detail string) {
		a.Indicators = append(a.Indicators, Indicator{ID: id, Severity: severity, Title: title, Detail: detail})
	}

	syms := make(map[string]bool, 256)
	for _, im := range img.Imports {
		a.ImportLibraries = append(a.ImportLibraries, im.Library)
		for _, s := range im.Symbols {
			syms[strings.ToLower(s)] = true
			a.ImportSymbolCount++
		}
	}
	a.ExportCount = len(img.Exports)

	if family, ok := knownPackerSection(img.Sections); ok {
		score += 40
		add("packer-section", "high", "已知加壳/保护节区名", "匹配到 "+family)
	}

	for _, s := range img.Sections {
		if s.RawSize < 4096 {
			continue
		}
		if s.Entropy >= 7.2 {
			a.HighEntropySections = append(a.HighEntropySections, fmt.Sprintf("%s %.3f", s.Name, s.Entropy))
		}
		if s.Writable && s.Executable {
			a.RWXSections = append(a.RWXSections, s.Name)
		}
	}
	if n := len(a.HighEntropySections); n > 0 {
		if n > 2 {
			score += 36
		} else {
			score += 18 * n
		}
		add("high-entropy", "medium", "节区熵接近随机",
			"高熵节区: "+strings.Join(a.HighEntropySections, ", ")+"；通常意味着压缩、加密或打包载荷")
	}
	if n := len(a.RWXSections); n > 0 {
		score += 12
		add("rwx-section", "high", "存在可写且可执行节区",
			"节区: "+strings.Join(a.RWXSections, ", ")+"；常见于自解压或运行时解密后跳转")
	}

	switch {
	case a.ImportSymbolCount == 0:
		if img.Format == "pe" {
			score += 25
			add("iat-empty", "high", "导入表为空", "PE 没有任何可解析的导入符号；优先怀疑加壳或手工构造的导入表")
		}
	case a.ImportSymbolCount < 12:
		score += 15
		add("iat-sparse", "medium", "导入表稀疏",
			fmt.Sprintf("仅 %d 个导入符号；正常程序通常远高于此，稀疏导入常见于壳或动态解析", a.ImportSymbolCount))
	}

	if img.EntryRVA != 0 && !entryInExecutable(img) {
		score += 20
		add("entry-outside-code", "high", "入口点不在可执行节区内",
			"入口地址落在无执行权限的区域；这是加壳后跳板（stub）的典型形态")
	}

	if img.OverlaySize > 65536 && img.Size > 0 && float64(img.OverlaySize)/float64(img.Size) > 0.2 {
		score += 8
		add("large-overlay", "low", "文件末尾有较大附加数据",
			fmt.Sprintf("overlay %d 字节，占文件 %.1f%%；可能是安装器数据，也可能是附加载荷",
				img.OverlaySize, 100*float64(img.OverlaySize)/float64(img.Size)))
	}

	if hits := apiHits(syms, dynamicResolveAPIs); len(hits) > 1 {
		score += 18
		add("dynamic-resolve", "medium", "运行时动态解析 API",
			"命中: "+strings.Join(hits, ", ")+"；导入表不能代表真实 API 集合")
	}

	if hits := apiHits(syms, antiDebugAPIs); len(hits) > 0 {
		score += 3 * len(hits)
		add("anti-debug-apis", "medium", "导入反调试相关 API",
			"命中: "+strings.Join(hits, ", ")+"；需在动态分析阶段逐点验证是否真的用于检测")
	}
	if hits := apiHits(syms, injectionAPIs); len(hits) > 0 {
		score += 2 * len(hits)
		add("injection-apis", "low", "导入注入/内存改写相关 API",
			"命中: "+strings.Join(hits, ", ")+"；亦常见于合法 JIT、插件系统与热更新")
	}

	if img.Format == "pe" {
		if rwx := emptyNameSections(img.Sections); len(rwx) > 0 {
			score += 10
			add("odd-section-name", "low", "节区名为空或含不可打印字符",
				"节区索引: "+strings.Join(rwx, ", "))
		}
	}

	for _, s := range img.Sections {
		if s.Name == ".text" && s.VSize > 0 && s.RawSize == 0 {
			score += 10
			add("text-not-backed", "high", "代码节只有虚拟大小没有文件数据",
				".text 在磁盘上为空，代码只能来自解压或映射；静态反汇编对此类样本无效")
			break
		}
	}

	if score > 100 {
		score = 100
	}
	switch {
	case score >= 60:
		a.Verdict = "packed"
	case score >= 30:
		a.Verdict = "suspect"
	default:
		a.Verdict = "clean"
	}
	a.PackerScore = score

	report := Crypto(img)
	for _, h := range report.Hits {
		a.Crypto = append(a.Crypto, h.ID+"@"+h.Section)
	}
	if len(report.Hits) > 0 {
		add("crypto-constants", "info", "发现已知算法常量",
			strings.Join(a.Crypto, ", ")+"；常量存在只说明表被编译进来，不说明被调用")
	}

	if a.Verdict == "packed" {
		a.StopLines = append(a.StopLines,
			"判定为加壳/自保护：字符串、常量与节区结论的覆盖率不可信。先运行到原始入口点并做内存取证，在此之前任何算法结论只能标 inferred。")
	}
	if a.ImportSymbolCount < 12 && img.Format == "pe" {
		a.StopLines = append(a.StopLines,
			"导入表稀疏：不要用导入列表推断程序能力；应在运行时 API 解析点采集真实集合。")
	}
	if a.Verdict == "packed" {
		a.NextSteps = append(a.NextSteps,
			"记录停止线：静态阶段到此为止，转入动态取证或换用未加壳的等价版本。")
	}
	if len(report.Hits) > 0 {
		a.NextSteps = append(a.NextSteps,
			"用 mode=crypto 取详细偏移，再确认该表是否被代码引用，最后用标准测试向量自证。")
	}
	if len(report.UnknownTables) > 0 {
		a.NextSteps = append(a.NextSteps,
			"存在未识别的高熵表（见 mode=crypto 的 unmatched_tables）；按自定义算法处理，先记录结构再命名。")
	}

	a.Limitations = append(a.Limitations,
		"容器、节区、导入导出与常量扫描均为纯静态读取；不反汇编、不执行样本。",
		"常量命中只证明该表存在于文件中，不证明被代码使用。",
		"熵按节采样（每节上限 8 MiB），末尾零填充会拉低数值。",
		"评分是启发式排序，不是结论；分值高不等于一定是壳，分值低也不等于未加壳。",
	)
	return a
}

func knownPackerSection(secs []Section) (string, bool) {
	for _, s := range secs {
		if family, ok := packerSections[strings.ToUpper(strings.TrimSpace(s.Name))]; ok {
			return family + " (" + s.Name + ")", true
		}
	}
	return "", false
}

func entryInExecutable(img *Image) bool {
	for _, s := range img.Sections {
		span := s.span()
		if span == 0 {
			continue
		}
		if s.Executable && img.EntryRVA >= s.RVA && img.EntryRVA < s.RVA+span {
			return true
		}
	}
	return false
}

func apiHits(syms map[string]bool, list []string) []string {
	var out []string
	for _, api := range list {
		if syms[strings.ToLower(api)] {
			out = append(out, api)
		}
	}
	return out
}

func emptyNameSections(secs []Section) []string {
	var out []string
	for _, s := range secs {
		if s.Name == "" {
			out = append(out, fmt.Sprintf("%d", s.Index))
			continue
		}
		for i := 0; i < len(s.Name); i++ {
			if s.Name[i] < 0x20 || s.Name[i] > 0x7e {
				out = append(out, fmt.Sprintf("%d", s.Index))
				break
			}
		}
	}
	return out
}
