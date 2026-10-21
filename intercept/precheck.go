package intercept

// 审批链的「规则优先」层。
//
// 判定的现状是:命中数据库里的正则规则 → 按规则办;没命中 → 交给 LLM 兜底判定。
// 于是「rm -rf /」这种教科书式的破坏、和「ls -la」这种纯只读命令,也要一次模型调用
// 才能得出结论——既烧钱,又让模型在确定性场景上有机会给出前后不一致的答案。
//
// Precheck 在模型之前用确定性规则判掉这两类:明确破坏 → deny,明确只读 → allow。
// 灰区(绝大多数真实渗透动作)仍然返回 ok=false,原样交给模型,策略不变。

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// destructiveRule 是一条「明确破坏」规则:命中即 deny,不进模型。
type destructiveRule struct {
	re     *regexp.Regexp
	reason string
}

// destructiveRules 只收与授权测试目标无关的、宿主级的不可逆破坏。漏判的代价是交给
// 模型(和原来一样),误判的代价是无辜的测试动作被拦——所以宁可少收几条。
var destructiveRules = []destructiveRule{
	{regexp.MustCompile(`(?i)\bmkfs(\.[a-z0-9]+)?\b`), "格式化文件系统"},
	{regexp.MustCompile(`(?i)\bdiskpart\b`), "磁盘分区操作"},
	{regexp.MustCompile(`(?i)\b(shutdown|poweroff|reboot|halt)\b`), "关机/重启宿主机"},
	{regexp.MustCompile(`(?i)\bdd\s+if=\S+\s+of=/dev/`), "直写块设备"},
	{regexp.MustCompile(`(?i)(^|[;&|]\s*)rm\s+-[a-z]*[rf][a-z]*\s+(/|~)(\s|$)`), "删除根目录/家目录"},
	{regexp.MustCompile(`(?i)(^|[;&|]\s*)del\s+/[fsq]\s+[a-z]:\\`), "递归删除盘符根目录"},
	{regexp.MustCompile(`(?i)\bvssadmin\s+delete\b`), "删除卷影副本"},
	{regexp.MustCompile(`(?i)\bcipher\s+/w\b`), "擦除磁盘空闲空间"},
}

// readOnlyCommands 是「确定不改状态」的命令白名单。判成 allow 前还会拒绝重定向、
// 管道、串联与写入类开关(见 isReadOnlyCommand)——白名单只描述命令本身。
var readOnlyCommands = map[string]bool{
	"ls": true, "dir": true, "pwd": true, "whoami": true, "id": true,
	"hostname": true, "uname": true, "date": true, "echo": true, "env": true,
	"cat": true, "type": true, "head": true, "tail": true, "wc": true,
	"ifconfig": true, "ipconfig": true, "netstat": true, "ps": true, "tasklist": true,
	"nslookup": true, "dig": true, "host": true, "ping": true, "tracert": true,
}

// commandToolBases 是「会执行外部命令」的工具名基名(小写)。只对这些工具做命令
// 文本预检,文件读写类工具(Read/Write/Edit)不在此列。
var commandToolBases = map[string]bool{
	"bash": true, "sh": true, "shell": true, "cmd": true,
	"execute_command": true, "run_command": true, "powershell": true, "pwsh": true,
}

// commandTool reports whether tool executes a shell command. MCP 工具名常见形态是
// mcp__server__bash,取最后一个分隔段比较。
func commandTool(tool string) bool {
	t := strings.ToLower(strings.TrimSpace(tool))
	if i := strings.LastIndex(t, "__"); i >= 0 {
		t = t[i+2:]
	}
	if i := strings.LastIndex(t, "."); i >= 0 {
		t = t[i+1:]
	}
	return commandToolBases[t]
}

// isReadOnlyCommand 只对「一条单纯的只读命令」返回 true:带重定向、管道、串联、
// 命令替换或写入类开关的一律不算。
func isReadOnlyCommand(cmd string) bool {
	if strings.ContainsAny(cmd, ">|&;`$\n\r") {
		return false
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	base := strings.ToLower(filepath.Base(fields[0]))
	base = strings.TrimSuffix(base, ".exe")
	if !readOnlyCommands[base] {
		return false
	}
	for _, f := range fields[1:] {
		switch strings.ToLower(f) {
		case "-o", "--output", "--outfile", "-outfile", "--write", "-w":
			return false
		}
	}
	return true
}

// Precheck 是判定链的规则优先层。ok=false 表示「没意见」,调用方继续原路径(交给
// 模型);ok=true 时返回的是终局判定,不需要模型。
func Precheck(tool string, arguments json.RawMessage) (Decision, bool) {
	if !commandTool(tool) {
		return Decision{}, false
	}
	var a struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"`
	}
	_ = json.Unmarshal(arguments, &a)
	cmd := strings.TrimSpace(a.Command)
	if cmd == "" {
		cmd = strings.TrimSpace(a.Cmd)
	}
	if cmd == "" {
		return Decision{}, false
	}
	low := strings.ToLower(cmd)
	for _, r := range destructiveRules {
		if r.re.MatchString(low) {
			return Decision{
				Action:     "deny",
				RuleName:   "precheck:destructive",
				Confidence: 1,
				Message:    "[预检] 拒绝：该命令属于宿主级不可逆破坏（" + r.reason + "），与授权测试目标无关。",
			}, true
		}
	}
	if isReadOnlyCommand(cmd) {
		return Decision{
			Action:     "allow",
			RuleName:   "precheck:readonly",
			Confidence: 0.95,
			Message:    "[预检] 放行：只读侦察命令，不改动任何状态。",
		}, true
	}
	return Decision{}, false
}
