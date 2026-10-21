package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// 只读扫描：把「这台机器上装了谁、配过谁、谁已经连上 ARTEX」算出来。
//
// 三条纪律：
//  1. 只 stat + 读文件，不执行任何客户端二进制。`--version` 会挂住（有的客户端
//     首次启动要拉远程配置），3 秒超时也救不了「用户点了扫描，页面卡死」。
//  2. 路径先展开再判断；展开失败的配置仍然展示，但标出来——用户才知道
//     是我们没找到，而不是他没装。
//  3. 不猜。找不到可执行文件就说找不到，不拿「配置存在」冒充「已安装」。

// HarnessConfigStatus 是单条配置文件的探测结果。
type HarnessConfigStatus struct {
	Scope      string `json:"scope"`
	Display    string `json:"display"` // 注册表原文（带 ~ 与变量）
	Path       string `json:"path"`    // 展开后的绝对路径
	Resolved   bool   `json:"resolved"`
	Format     string `json:"format"`
	Strategy   string `json:"strategy"`
	Note       string `json:"note,omitempty"`
	Exists     bool   `json:"exists"`
	Writable   bool   `json:"writable"`
	HasArtex   bool   `json:"has_artex"`
	SizeBytes  int64  `json:"size_bytes"`
	Unreadable string `json:"unreadable,omitempty"`
}

// HarnessClientStatus 是一个 harness 的完整探测结果。
type HarnessClientStatus struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Vendor      string `json:"vendor"`
	Kind        string `json:"kind"`
	Src         string `json:"src"`
	MCP         string `json:"mcp"`
	Tier        string `json:"tier"`
	Artex       string `json:"artex"`
	Restart     bool   `json:"restart"`
	NeedsVerify bool   `json:"needs_verify"`
	Notes       string `json:"notes"`
	Doc         string `json:"doc"`

	BinPath string `json:"bin_path,omitempty"`
	// State: linked（已配 artex）/ configured（有配置没 artex）/ installed（装了没配）/ absent
	State string `json:"state"`
	// Stale 表示「配置文件在，但找不到程序，也没有安装目录」——多半是卸载残留。
	Stale   bool                  `json:"stale"`
	Configs []HarnessConfigStatus `json:"configs"`
}

const harnessMaxReadBytes = 1 << 20 // 1 MiB：配置文件再大也当读不了，不拖慢扫描

// harnessScanAll 扫描全部客户端。projectDir 为空时，项目级配置只报路径不判存在。
func harnessScanAll(projectDir string) []HarnessClientStatus {
	reg, err := HarnessRegistryData()
	if err != nil || reg == nil {
		return nil
	}
	out := make([]HarnessClientStatus, 0, len(reg.Clients))
	for _, c := range reg.Clients {
		out = append(out, harnessScanOne(c, projectDir))
	}
	return out
}

func harnessScanOne(c HarnessClient, projectDir string) HarnessClientStatus {
	st := HarnessClientStatus{
		ID: c.ID, Name: c.Name, Vendor: c.Vendor, Kind: c.Kind, Src: c.Src,
		MCP: c.MCP, Tier: c.Tier, Artex: c.Artex, Restart: c.Restart,
		NeedsVerify: c.NeedsVerify, Notes: c.Notes, Doc: c.Doc,
		Configs: make([]HarnessConfigStatus, 0, len(c.Config)),
	}

	var hasConfig, hasArtex bool
	for _, cfg := range c.Config {
		if !harnessConfigForOS(cfg) {
			continue
		}
		row := harnessProbeConfig(cfg, projectDir)
		if row.Exists {
			hasConfig = true
		}
		if row.HasArtex {
			hasArtex = true
		}
		st.Configs = append(st.Configs, row)
	}

	// 可执行文件：只看 PATH，不执行。
	for _, b := range c.Detect.Bins {
		if p, err := exec.LookPath(b); err == nil {
			st.BinPath = p
			break
		}
	}

	dirFound := false
	for _, p := range append(append([]string{}, c.Detect.Paths...), c.Detect.Apps...) {
		abs, ok := harnessExpandPath(p)
		if !ok {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			dirFound = true
			break
		}
	}

	switch {
	case hasArtex:
		st.State = "linked"
	case hasConfig:
		st.State = "configured"
	case st.BinPath != "" || dirFound:
		st.State = "installed"
	default:
		st.State = "absent"
	}
	// 卸载残留：配置在，但既没有二进制也没有任何目录。
	st.Stale = hasConfig && st.BinPath == "" && !dirFound && (len(c.Detect.Bins) > 0 || len(c.Detect.Apps) > 0)
	return st
}

func harnessProbeConfig(cfg HarnessConfig, projectDir string) HarnessConfigStatus {
	row := HarnessConfigStatus{
		Scope: cfg.Scope, Display: cfg.Path, Format: cfg.Format,
		Strategy: cfg.Strategy, Note: cfg.Note,
	}
	if cfg.Strategy == "manual" {
		return row
	}

	abs, ok := harnessResolveConfigPath(cfg, projectDir)
	row.Path, row.Resolved = abs, ok
	if !ok {
		return row
	}
	row.Writable = harnessScopeWritable(cfg.Scope) || (projectDir != "" && abs != "")

	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		return row
	}
	row.Exists = true
	row.SizeBytes = fi.Size()

	if fi.Size() > harnessMaxReadBytes {
		row.Unreadable = "文件超过 1 MiB，跳过内容检查"
		return row
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		row.Unreadable = err.Error()
		return row
	}
	row.HasArtex = harnessHasArtexEntry(string(data), cfg)
	return row
}

// harnessResolveConfigPath 把注册表路径落到磁盘。
//
// 项目级（project/workspace）相对路径跟着 projectDir 走；没给 projectDir 就
// 原样返回相对路径，Exists 一定是 false —— 不拿控制台的 CWD 去猜用户的项目。
func harnessResolveConfigPath(cfg HarnessConfig, projectDir string) (string, bool) {
	raw := strings.TrimSpace(cfg.Path)
	relative := !filepath.IsAbs(raw) && !strings.HasPrefix(raw, "~") && !strings.Contains(raw, "$") && !strings.Contains(raw, "%")
	if relative {
		if projectDir == "" {
			return "", false
		}
		abs, err := filepath.Abs(filepath.Join(projectDir, raw))
		if err != nil {
			return "", false
		}
		return abs, true
	}
	return harnessExpandPath(raw)
}

var (
	reTomlArtex    = regexp.MustCompile(`(?m)^\s*\[mcp_servers\.artex\]`)
	reYAMLArtexKey = regexp.MustCompile(`(?m)^\s*artex:\s*$`)
	reCordisArtex  = regexp.MustCompile(`(?m)^\s*(id:\s*mcp-artex|serverName:\s*artex)\b`)
)

// harnessHasArtexEntry 判断「这份配置里已经有 artex 了」。
//
// 按方言分别判，不用全文件 grep：注释里写一句 "artex" 不该被当成已接入。
// 但 JSONC 解析可能失败（用户手写注释写坏了），退化成子串匹配，宁可误报
// 「已接入」也不误报「没接」——后者会让用户重复写入。
func harnessHasArtexEntry(content string, cfg HarnessConfig) bool {
	switch cfg.Format {
	case "json", "jsonc":
		key := cfg.Key
		if key == "" {
			key = "mcpServers"
		}
		if strings.Contains(content, `"`+key+`"`) && strings.Contains(content, `"artex"`) {
			return true
		}
		return false
	case "toml":
		return reTomlArtex.MatchString(content)
	case "yaml":
		return reYAMLArtexKey.MatchString(content)
	case "cordis-yaml":
		return reCordisArtex.MatchString(content)
	}
	return strings.Contains(content, "artex")
}

// harnessStats 给前端做汇总条。
func harnessStats(clients []HarnessClientStatus) map[string]int {
	stats := map[string]int{"total": len(clients), "linked": 0, "configured": 0, "installed": 0, "absent": 0}
	for _, c := range clients {
		stats[c.State]++
	}
	return stats
}
