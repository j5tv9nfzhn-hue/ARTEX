package server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// 外部 harness 注册表：谁存在、配置在哪、用什么方言、能不能自动写。
//
// 这是「一键配置」的唯一真相。它不放在前端，也不靠文档约定——扫描、预览、
// 写入三条路径读的是同一份数据；加一个客户端只改 JSON，不改代码。
//
// 三个等级不是质量分级，是**证据分级**：
//   A 官方文档逐字核对过，敢自动写；
//   B 只有社区来源，只做只读探测，写入按钮灰着并标「需确认」；
//   C 没有可写的落点（UI 配置 / 本身是 MCP 服务端），只给片段。
//
// 一条硬规则：路径一律写成 `~` / `$VAR` / `%VAR%` 形式，落到磁盘前再展开。
// 注册表里出现本机绝对路径，就意味着这份 JSON 只能在一台机器上用。

//go:embed harness_registry.json
var harnessRegistryRaw []byte

type HarnessRegistry struct {
	Version     int             `json:"version"`
	GeneratedAt string          `json:"generated_at"`
	Note        string          `json:"note"`
	Clients     []HarnessClient `json:"clients"`
}

// HarnessClient 是注册表里的一行。
type HarnessClient struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Vendor      string          `json:"vendor"`
	Kind        string          `json:"kind"` // cli / ide / gui / cli+web
	Src         string          `json:"src"`  // open / closed
	MCP         string          `json:"mcp"`  // native / plugin / none
	Tier        string          `json:"tier"` // A / B / C
	Detect      HarnessDetect   `json:"detect"`
	Config      []HarnessConfig `json:"config"`
	Remote      HarnessRemote   `json:"remote"`
	Restart     bool            `json:"restart"`
	Artex       string          `json:"artex"` // http / stdio / manual
	NeedsVerify bool            `json:"needs_verify"`
	Notes       string          `json:"notes"`
	Doc         string          `json:"doc"`
}

type HarnessDetect struct {
	Bins  []string `json:"bins"`
	Paths []string `json:"paths"`
	Apps  []string `json:"apps"`
}

type HarnessConfig struct {
	Scope    string   `json:"scope"`    // user / project / workspace / machine
	Path     string   `json:"path"`     // 带 ~ / $VAR / %VAR% / <profile>
	Format   string   `json:"format"`   // json / jsonc / toml / yaml / cordis-yaml / ui
	Key      string   `json:"key"`      // 根键或表头，人读用
	Strategy string   `json:"strategy"` // file / file-append / cli-only / yaml-insert / manual
	Note     string   `json:"note"`
	OS       []string `json:"os"` // 空=全平台
}

type HarnessRemote struct {
	Field     string `json:"field"`
	TypeField string `json:"type_field"` // 形如 type=http / transport=streamable-http
	Headers   bool   `json:"headers"`
	VarSyntax string `json:"var_syntax"`
	Auth      string `json:"auth"`
}

var (
	harnessOnce sync.Once
	harnessReg  *HarnessRegistry
	harnessErr  error
)

// HarnessRegistryData 返回进程内只读的注册表；解析或校验失败会一直返回同一个错误。
//
// 缓存是有意的：这份文件编进二进制，运行期不可能变。校验放在加载时做，
// 等于把「注册表写错」提前到启动后的第一次访问，而不是写到用户磁盘上才发现。
func HarnessRegistryData() (*HarnessRegistry, error) {
	harnessOnce.Do(func() {
		var reg HarnessRegistry
		if err := json.Unmarshal(harnessRegistryRaw, &reg); err != nil {
			harnessErr = fmt.Errorf("harness 注册表不是合法 JSON: %w", err)
			return
		}
		if err := reg.Validate(); err != nil {
			harnessErr = err
			return
		}
		harnessReg = &reg
	})
	return harnessReg, harnessErr
}

var (
	harnessTiers     = map[string]bool{"A": true, "B": true, "C": true}
	harnessArtex     = map[string]bool{"http": true, "stdio": true, "manual": true}
	harnessFormats   = map[string]bool{"json": true, "jsonc": true, "toml": true, "yaml": true, "cordis-yaml": true, "ui": true, "": true}
	harnessStrategy  = map[string]bool{"file": true, "file-append": true, "cli-only": true, "yaml-insert": true, "manual": true, "": true}
	errHarnessBadReg = errors.New("harness 注册表校验失败")
)

// Validate 校验结构约束。宁可启动时报错，也不要带着坏数据去写用户的配置文件。
func (r *HarnessRegistry) Validate() error {
	if r.Version <= 0 {
		return fmt.Errorf("%w: 缺少 version", errHarnessBadReg)
	}
	if len(r.Clients) == 0 {
		return fmt.Errorf("%w: clients 为空", errHarnessBadReg)
	}
	seen := make(map[string]bool, len(r.Clients))
	for _, c := range r.Clients {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("%w: 有客户端缺少 id", errHarnessBadReg)
		}
		if seen[c.ID] {
			return fmt.Errorf("%w: 重复的 id %q", errHarnessBadReg, c.ID)
		}
		seen[c.ID] = true
		if !harnessTiers[c.Tier] {
			return fmt.Errorf("%w: %s 的 tier=%q 非法", errHarnessBadReg, c.ID, c.Tier)
		}
		if !harnessArtex[c.Artex] {
			return fmt.Errorf("%w: %s 的 artex=%q 非法", errHarnessBadReg, c.ID, c.Artex)
		}
		if len(c.Config) == 0 && c.Artex != "manual" {
			return fmt.Errorf("%w: %s 没有 config 却声明 artex=%s", errHarnessBadReg, c.ID, c.Artex)
		}
		for _, cfg := range c.Config {
			if !harnessFormats[cfg.Format] {
				return fmt.Errorf("%w: %s 的 format=%q 非法", errHarnessBadReg, c.ID, cfg.Format)
			}
			if !harnessStrategy[cfg.Strategy] {
				return fmt.Errorf("%w: %s 的 strategy=%q 非法", errHarnessBadReg, c.ID, cfg.Strategy)
			}
			if strings.TrimSpace(cfg.Path) == "" && cfg.Strategy != "manual" {
				return fmt.Errorf("%w: %s 有 config 缺少 path", errHarnessBadReg, c.ID)
			}
		}
	}
	return nil
}

// Client 按 id 取一行。
func (r *HarnessRegistry) Client(id string) (HarnessClient, bool) {
	for _, c := range r.Clients {
		if c.ID == id {
			return c, true
		}
	}
	return HarnessClient{}, false
}

var (
	rePercentVar = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)
	reBraceVar   = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	reBareVar    = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
)

// harnessEnvDefaults 给「声明了 $X_HOME 但用户没设」的客户端兜底。
//
// 这些 harness 的默认值都由官方约定给死（~/.codex、~/.dsh …），不兜底的话
// 扫描会把「装了但没设环境变量」误判成「没装」。
func harnessEnvDefaults() map[string]string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil
	}
	return map[string]string{
		"CODEX_HOME":      filepath.Join(home, ".codex"),
		"DSH_HOME":        filepath.Join(home, ".dsh"),
		"COPILOT_HOME":    filepath.Join(home, ".copilot"),
		"GROK_HOME":       filepath.Join(home, ".grok"),
		"KIMI_CODE_HOME":  filepath.Join(home, ".kimi-code"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
	}
}

// harnessProfile 是 DSH 的 profile 名；注册表里用 <profile> 占位。
func harnessProfile() string {
	if v := strings.TrimSpace(os.Getenv("DSH_PROFILE")); v != "" {
		return v
	}
	return "web"
}

// harnessExpandPath 展开 ~ / $VAR / ${VAR} / %VAR% / <profile>。
//
// 返回 ok=false 表示还有没解析掉的占位符（比如根本不是本机的环境变量）。
// 这种情况下不许写盘：路径不可解就等于目标不确定。
func harnessExpandPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	defaults := harnessEnvDefaults()
	lookup := func(name string) (string, bool) {
		if v, ok := os.LookupEnv(name); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), true
		}
		if v, ok := defaults[name]; ok {
			return v, true
		}
		return "", false
	}

	out := p
	var missing bool
	out = rePercentVar.ReplaceAllStringFunc(out, func(m string) string {
		if v, ok := lookup(m[1 : len(m)-1]); ok {
			return v
		}
		missing = true
		return m
	})
	out = reBraceVar.ReplaceAllStringFunc(out, func(m string) string {
		if v, ok := lookup(m[2 : len(m)-1]); ok {
			return v
		}
		missing = true
		return m
	})
	out = reBareVar.ReplaceAllStringFunc(out, func(m string) string {
		if v, ok := lookup(m[1:]); ok {
			return v
		}
		missing = true
		return m
	})
	out = strings.ReplaceAll(out, "<profile>", harnessProfile())

	if strings.HasPrefix(out, "~") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		switch {
		case out == "~":
			out = home
		case strings.HasPrefix(out, "~/"), strings.HasPrefix(out, `~\`):
			out = filepath.Join(home, out[2:])
		}
	}
	if missing || strings.ContainsAny(out, "%$<>") {
		return out, false
	}
	if runtime.GOOS == "windows" {
		out = filepath.FromSlash(out)
	}
	return out, true
}

// harnessConfigForOS 判断这条配置是否属于当前平台。
func harnessConfigForOS(cfg HarnessConfig) bool {
	if len(cfg.OS) == 0 {
		return true
	}
	for _, o := range cfg.OS {
		if strings.EqualFold(o, runtime.GOOS) {
			return true
		}
	}
	return false
}

// harnessScopeWritable 说明这个作用域能不能由我们写。
//
// project/workspace 是「用户的项目目录」，控制台不知道是哪个——除非用户在
// 请求里显式给 project_dir。给不出来就只出片段，绝不往 ARTEX 的安装目录里写。
func harnessScopeWritable(scope string) bool {
	switch scope {
	case "user", "machine":
		return true
	}
	return false
}
