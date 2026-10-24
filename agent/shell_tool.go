package agent

// Bash 工具与执行器之间的适配层（任务 26）。
//
// 职责只有四件：把请求送进 runShell、把结果转成 actool.Result（含截断与落盘）、
// 在失败时给出可行动的 error_class + 修复建议、以及把 8 类错误交给指标层。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// runShellTool 执行一条命令并产出工具结果。
func runShellTool(ctx context.Context, command, workDir string, extraEnv []string, tc *actool.ToolContext, timeout time.Duration) actool.Result {
	req := shellRequest{Command: command, WorkDir: workDir, Timeout: timeout}
	if tc != nil {
		if req.WorkDir == "" {
			req.WorkDir = tc.WorkingDir
		}
		req.Env = append(req.Env, tc.Env...)
	}
	// agent 级的额外环境（worker 的代理 + CA）排在会话环境之后：os/exec 对同名键
	// 保留最后一次出现，所以代理配置会覆盖会话里的同名变量——这正是期望的优先级。
	// 历史实现只在「直调二进制」路径生效，Shell 路径漏了，等于 worker 里所有经 Shell
	// 发的请求都绕过代理。
	req.Env = append(req.Env, extraEnv...)
	out := runShell(ctx, req)

	text := out.Output
	if text != "" {
		text = ensureUTF8Output(text)
	}

	// 执行器自身失败（Shell 不可用 / 超时 / 会话崩了）：这不属于「命令失败」，
	// 必须与退出码非零区分开，否则模型会反复重试一条永远跑不通的命令。
	if out.Err != nil {
		class := ClassifyShellFailure(out)
		msg := shellErrorHint(class, command, out)
		if text != "" {
			msg = text + "\n\n" + msg
		}
		return actool.Errorf(captureToolOutput(tc, msg))
	}

	if strings.TrimSpace(text) == "" {
		text = "(command produced no output)"
	}
	if out.ExitCode != 0 {
		return actool.Errorf(fmt.Sprintf("%s\n\n[exit code %d]", captureToolOutput(tc, text), out.ExitCode))
	}
	return actool.Text(captureToolOutput(tc, text))
}

// shellErrorClass* 是 8 类错误（任务 26 步骤 ⑤）。取值刻意保持低基数，
// 便于直接在 tool_usage.error_class 上做聚合（对齐 OpenTelemetry GenAI 的
// error.type 约定：低基数、可聚合、不塞自由文本）。
const (
	errClassNone      = ""
	ErrClassConfig    = "config"    // 执行器/设置有问题（Shell 不存在、模式不合法）
	ErrClassDialect   = "dialect"   // 写法与执行器不匹配（bash 语法喂给 PS）
	ErrClassNotFound  = "not_found" // 命令/文件/目标不存在
	ErrClassParam     = "param"     // 参数不合法（缺必填、类型错、flag 拼错）
	ErrClassPerm      = "perm"      // 权限不足（ACL / 只读 / 需管理员）
	ErrClassEnv       = "env"       // 环境依赖缺失（python 未装、PATH 不含该工具）
	ErrClassTimeout   = "timeout"   // 超时/被取消
	ErrClassTransport = "transport" // 网络/代理层失败
	// ErrClassQuota 是第 9 类、且不属于 Shell：本条意图的工具额度用尽、调用被闸门
	// 拒绝（任务 27）。它必须单独成类——否则「额度打满」会被算进 Bash 的错误率里，
	// 把真实失败率污染掉，也无法据此调阈值。
	ErrClassQuota = "quota"
)

// ClassifyShellFailure 把一次执行结果归到 8 类之一。返回空串表示「命令正常失败」
// （例如测试失败、grep 无匹配），不需要额外归因。
func ClassifyShellFailure(out shellOutcome) string {
	if out.TimedOut {
		return ErrClassTimeout
	}
	if out.Err != nil {
		if errors.Is(out.Err, ErrShellNotConfigured) || errors.Is(out.Err, ErrShellNotAvailable) {
			return ErrClassConfig
		}
	}
	spec := CurrentShellSpec()
	body := strings.ToLower(out.Output)
	if body == "" {
		return errClassNone
	}
	// 配置类：执行器本身有问题（最优先，因为它会让所有命令一起失败）。
	if spec.Zero() && looksLikeConfigFailure(body) {
		return ErrClassConfig
	}
	// 写法不符：bash 语法喂给 PowerShell，或 PowerShell 语法喂给 cmd。
	// 未解析出执行器时用全集判断——宁可多认「写法问题」，也别把它算成环境缺失。
	if looksLikeDialectText(body) {
		return ErrClassDialect
	}
	if spec.Mode == ShellModePowerShell || spec.Mode == ShellModeCmd {
		for _, sig := range dialectSignalsFor(spec) {
			if strings.Contains(body, strings.ToLower(sig)) {
				return ErrClassDialect
			}
		}
	}
	if looksLikeParamFailure(body) {
		return ErrClassParam
	}
	if looksLikePermFailure(body) {
		return ErrClassPerm
	}
	if looksLikeTransportFailure(out.Output) {
		return ErrClassTransport
	}
	if looksLikeEnvFailure(body) {
		return ErrClassEnv
	}
	if looksLikeNotFoundFailure(body) {
		return ErrClassNotFound
	}
	// 退出码 127（bash）/ 9009（cmd）几乎总是「命令不存在」。
	if out.ExitCode == 127 || out.ExitCode == 9009 {
		return ErrClassNotFound
	}
	return errClassNone
}

func looksLikeConfigFailure(body string) bool {
	for _, sig := range []string{
		"not recognized as the name of a cmdlet",
		"无法将",
		"不是内部或外部命令",
		"系统找不到指定的文件",
	} {
		if strings.Contains(body, strings.ToLower(sig)) {
			return true
		}
	}
	return false
}

func looksLikeParamFailure(body string) bool {
	for _, sig := range []string{
		"missing required argument",
		"requires a value",
		"cannot bind argument",
		"unrecognized arguments",
		"invalid argument",
		"invalid option",
		"unknown option",
		"unknown flag",
		"invalid flag",
		"usage:",
		"参数错误",
		"缺少参数",
		"invalid value",
	} {
		if strings.Contains(body, sig) {
			return true
		}
	}
	return false
}

func looksLikePermFailure(body string) bool {
	for _, sig := range []string{
		"permission denied",
		"access is denied",
		"拒绝访问",
		"权限不够",
		"requires elevation",
		"must be run as administrator",
		"eacces",
	} {
		if strings.Contains(body, sig) {
			return true
		}
	}
	return false
}

func looksLikeEnvFailure(body string) bool {
	for _, sig := range []string{
		"'python' is not recognized",
		"python: command not found",
		"python was not found",
		"no module named",
		"modulenotfounderror",
		"cannot find module",
		"command not found",
		"找不到可运行的",
	} {
		if strings.Contains(body, sig) {
			return true
		}
	}
	return false
}

func looksLikeNotFoundFailure(body string) bool {
	for _, sig := range []string{
		"no such file or directory",
		"cannot find path",
		"找不到路径",
		"系统找不到指定的路径",
		"itemnotfoundexception",
		"no such file",
		"file not found",
		"could not resolve host",
	} {
		if strings.Contains(body, sig) {
			return true
		}
	}
	return false
}

// shellErrorHint 把 error_class 翻成一句「下一步该做什么」。
//
// 这是压错误率的关键一步：模型看到 403 条同样的报错文本会换个写法再试一次；
// 看到「执行器不可用，别重试 Bash」才会真的换路径。措辞按 class 分派，不写套话。
func shellErrorHint(class, command string, out shellOutcome) string {
	switch class {
	case ErrClassConfig:
		msg := "【配置错误：执行器不可用，重试不会成功】"
		if out.Err != nil {
			msg += out.Err.Error() + "。"
		}
		msg += "请在「设置 → 工具执行 → Shell」里选择一个本机确实存在的 Shell，或设置环境变量 ARTEX_SHELL_BIN 指向可执行文件后重启。"
		msg += "在此之前不要重试 Bash：可先用 Read / Grep / Glob 读文件；需要联网或执行命令时，必须先修好执行器。"
		return msg
	case ErrClassTimeout:
		return "【超时】命令在预算内没有结束，已被终止（不是命令写错了）。" +
			"需要长时间运行的任务请用 run_in_background=true 重新发起；不要原样重试，也不要缩短命令后连发多次。"
	case ErrClassDialect:
		return "【写法与执行器不匹配】这条命令不是目标有问题，而是语法属于别的 Shell。按上面的「执行器契约」改写一次即可；" +
			"不确定语法时，用 python -c \"...\" 或内置工具，不要连续试多种写法。"
	default:
		if out.Err != nil {
			return "【执行器失败】" + out.Err.Error() + "。"
		}
		return ""
	}
}

// ErrorClassLegend 返回 8 类的界面文案（设置页/统计页展示用）。
func ErrorClassLegend() []map[string]string {
	return []map[string]string{
		{"class": ErrClassConfig, "label": "执行器配置不可用"},
		{"class": ErrClassDialect, "label": "写法与执行器不匹配"},
		{"class": ErrClassNotFound, "label": "命令/文件/目标不存在"},
		{"class": ErrClassParam, "label": "参数不合法"},
		{"class": ErrClassPerm, "label": "权限不足"},
		{"class": ErrClassEnv, "label": "环境依赖缺失"},
		{"class": ErrClassTimeout, "label": "超时/被取消"},
		{"class": ErrClassTransport, "label": "网络/代理失败"},
		{"class": ErrClassQuota, "label": "意图工具额度用尽"},
	}
}

// ErrorClassOf 计算一次工具调用该记哪个 error_class（写进 tool_usage）。
//
// 之所以放在「结果文本」上判断而不是让执行器带出来：actool.Result 是 SDK 类型，
// 加字段要改依赖；而账本只需要一个低基数标签，从结果文本 + 调用上下文就能稳定推导。
// 判定顺序刻意从「环境」到「内容」：先排除执行器/超时/网络这类与命令内容无关的原因，
// 剩下的才归因到命令本身，否则错误率永远算不清。
func ErrorClassOf(res actool.Result, err error, callCtx context.Context) string {
	if err != nil {
		if callCtx != nil && (callCtx.Err() == context.DeadlineExceeded || callCtx.Err() == context.Canceled) {
			return ErrClassTimeout
		}
		return ErrClassEnv
	}
	if !res.IsError {
		return errClassNone
	}
	return classifyErrorText(res.Flatten())
}

// ClassifyToolError 是「只有 error、没有结果文本」的归因入口（MCP 审计事件走这条）。
// 与控制台内部调用共用同一套判定链，保证账本里的 error_class 口径一致。
func ClassifyToolError(err error) string {
	if err == nil {
		return errClassNone
	}
	return classifyErrorText(err.Error())
}

// classifyErrorText 是 8 类判定的唯一实现：从结果/错误文本推导类别。
func classifyErrorText(text string) string {
	lower := strings.ToLower(text)
	// 额度闸门的拒绝最优先：它不是工具/执行器的问题，是预算问题。
	if strings.HasPrefix(strings.TrimSpace(text), "【额度】") {
		return ErrClassQuota
	}
	if strings.Contains(text, "【配置错误：执行器不可用") {
		return ErrClassConfig
	}
	if strings.Contains(text, "【超时】") || strings.Contains(text, "命令超时") || strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout") {
		return ErrClassTimeout
	}
	if strings.Contains(text, "【写法与执行器不匹配】") {
		return ErrClassDialect
	}
	if looksLikeTransportFailure(text) {
		return ErrClassTransport
	}
	if looksLikeDialectText(lower) {
		return ErrClassDialect
	}
	if looksLikeParamFailure(lower) {
		return ErrClassParam
	}
	if looksLikePermFailure(lower) {
		return ErrClassPerm
	}
	if looksLikeEnvFailure(lower) {
		return ErrClassEnv
	}
	if looksLikeNotFoundFailure(lower) {
		return ErrClassNotFound
	}
	return ErrClassEnv // 兜底：可归因于执行环境而非工具缺陷
}

// looksLikeDialectText 是跨模式通用的「语法不属于本执行器」信号。
func looksLikeDialectText(lower string) bool {
	for _, sig := range []string{
		"not recognized as the name of a cmdlet",
		"不是内部或外部命令",
		"syntax error near unexpected token",
		"意外的标记",
		"the term '",
		"commandnotfoundexception",
		"有效语句分隔符",                            // PS 5.1 遇到 && 的原话
		"is not a valid statement separator", // 同上（英文）
		"the ampersand operator",
	} {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}
