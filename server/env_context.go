package server

// 批次 6 / #10、#19、#22：共享环境上下文。
//
// 平台在任务准入时对「这台机器」做一次探测（OS、Bash 工具实际执行器、PATH 上的
// shell、常用工具版本、数据/技能目录可写性），落库为 task_env_context，并把压缩
// 后的描述注入该任务全部 planner/worker 的 system prompt。三个条目对应三件事：
//   - #10 环境依赖：把「任务依赖什么」从隐式试错变成显式快照 + 契约校验；
//   - #19 共享上下文：同一任务的所有 agent 看到同一份环境描述，不再各猜各的；
//   - #22 环境契约：只校验平台真正依赖的项（shell 可用、目录可写），失败给出
//     中文警告并写入时间线，而不是让第一次 Bash 调用神秘失败。
//
// 探测结果进程内缓存（工具版本不会在一次运行中变化）；契约校验每次准入重做，
// 因为目录可写性可能被磁盘/权限变化打破。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
)

// envToolVersion is one PATH-resolved tool with its first --version line.
type envToolVersion struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// envSnapshot is the persisted/displayable probe result. Field names must stay
// stable: db.ExplorationEnvDigest copies a subset (os/arch/executor/shells/tools/
// work_dir) onto exploration nodes (#20).
type envSnapshot struct {
	OS         string           `json:"os"`
	Arch       string           `json:"arch"`
	Executor   string           `json:"executor"`
	Shells     []string         `json:"shells,omitempty"`
	DataDir    string           `json:"data_dir"`
	SkillDir   string           `json:"skill_dir"`
	WorkDir    string           `json:"work_dir,omitempty"`
	Tools      []envToolVersion `json:"tools,omitempty"`
	CapturedAt string           `json:"captured_at"`
}

// envToolCandidates are probed once per process (LookPath + one --version line).
// Kept deliberately small: the version strings are prompt context, not an
// inventory — missing tools are simply absent.
var envToolCandidates = []string{"python", "python3", "pip", "node", "npm", "go", "git", "curl", "nmap", "sqlmap", "jq"}

var (
	envToolsOnce   sync.Once
	envToolsCached []envToolVersion
)

func probeEnvTools() []envToolVersion {
	envToolsOnce.Do(func() {
		out := make([]envToolVersion, 0, len(envToolCandidates))
		for _, name := range envToolCandidates {
			if _, err := exec.LookPath(name); err != nil {
				continue
			}
			out = append(out, envToolVersion{Name: name, Version: probeVersionLine(name)})
		}
		envToolsCached = out
	})
	return envToolsCached
}

// probeVersionLine runs "<tool> --version" with a hard 500ms cap and returns the
// first non-empty line (e.g. "Python 3.12.1 (main, ...)"). Best-effort: any
// failure → "".
func probeVersionLine(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, "--version")
	b, err := cmd.CombinedOutput()
	if err != nil && len(b) == 0 {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 120 {
				line = line[:120]
			}
			return line
		}
	}
	return ""
}

// sharedEnvSnapshot builds the (cheap, cached where it matters) probe for one task.
func (s *Server) sharedEnvSnapshot(taskID string) envSnapshot {
	sc := shellCapability()
	env := envSnapshot{
		OS:         sc.OS,
		Arch:       runtime.GOARCH,
		Executor:   sc.Executor,
		Shells:     sc.Available,
		Tools:      probeEnvTools(),
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if s.m != nil {
		env.DataDir = s.m.dir
		env.WorkDir = filepath.Join(s.m.dir, "tasks", taskID)
	}
	if s.skillDir != "" {
		env.SkillDir = s.skillDir
	}
	return env
}

// checkEnvContract validates the environment facts the platform actually relies
// on: an executable shell for the Bash tool, and writable data/skill dirs. Returns
// the machine-readable contract plus operator-readable Chinese warnings.
func (s *Server) checkEnvContract(env envSnapshot) (map[string]any, []string) {
	shellOK := shellExecutorUsable()
	contract := map[string]any{"shell_usable": shellOK}
	warnings := []string{}
	if !shellOK {
		warnings = append(warnings, fmt.Sprintf("未在 PATH 找到 %s 解释器，Bash 工具将无法执行命令", env.Executor))
	}
	if env.DataDir != "" {
		dataOK := dirWritable(env.DataDir)
		contract["data_dir_writable"] = dataOK
		if !dataOK {
			warnings = append(warnings, "数据目录不可写："+env.DataDir)
		}
		transcriptsOK := dirWritable(filepath.Join(env.DataDir, "transcripts"))
		contract["transcripts_dir_writable"] = transcriptsOK
		if !transcriptsOK {
			warnings = append(warnings, "会话记录目录不可写："+filepath.Join(env.DataDir, "transcripts"))
		}
	}
	if env.SkillDir != "" {
		skillOK := dirWritable(env.SkillDir)
		contract["skill_dir_writable"] = skillOK
		if !skillOK {
			warnings = append(warnings, "技能目录不可写："+env.SkillDir)
		}
	}
	return contract, warnings
}

func shellExecutorUsable() bool {
	var candidates []string
	if runtime.GOOS == "windows" {
		candidates = []string{"pwsh", "powershell"}
	} else {
		candidates = []string{"bash", "sh"}
	}
	for _, name := range candidates {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".artex-writecheck-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

// captureTaskEnvContext probes, persists and returns the injectable brief for one
// task. Called on every admission (inside the admission lock — first call pays the
// one-time version probe). A changed warning set is surfaced once into the task
// timeline so operators see environment breakage without reading logs.
func (s *Server) captureTaskEnvContext(t *Task) string {
	if t == nil || s.m == nil || s.m.pg == nil {
		return ""
	}
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || taskID <= 0 {
		return ""
	}
	env := s.sharedEnvSnapshot(t.ID)
	contract, warnings := s.checkEnvContract(env)
	ctx, cancel := context.WithTimeout(context.Background(), sqliteWriteTimeout)
	defer cancel()
	prev, _ := s.m.pg.GetTaskEnvContext(ctx, taskID)
	if err := s.m.pg.UpsertTaskEnvContext(ctx, taskID, "probe", env, contract, warnings); err != nil {
		log.Printf("[env] task %s 环境快照落库失败: %v", t.ID, err)
	}
	if len(warnings) > 0 {
		log.Printf("[env] task %s 环境契约警告: %s", t.ID, strings.Join(warnings, "；"))
		var prevWarnings []string
		if prev != nil {
			prevWarnings = prev.Warnings
		}
		if strings.Join(prevWarnings, "；") != strings.Join(warnings, "；") {
			s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
				Summary: "环境契约警告：" + strings.Join(warnings, "；")})
		}
	}
	return renderEnvBrief(env, warnings)
}

// taskEnvBrief renders the persisted snapshot for prompt injection (single source
// of truth: agents read the row admission wrote). Falls back to a live probe when
// the row is missing (an agent bundle built before the first admission).
func (s *Server) taskEnvBrief(taskID string) string {
	if s.m == nil || s.m.pg == nil || taskID == "" {
		return ""
	}
	id, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || id <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if snap, err := s.m.pg.GetTaskEnvContext(ctx, id); err == nil && snap != nil {
		var env envSnapshot
		if b, err := json.Marshal(snap.Env); err == nil {
			_ = json.Unmarshal(b, &env)
		}
		return renderEnvBrief(env, snap.Warnings)
	}
	env := s.sharedEnvSnapshot(taskID)
	_, warnings := s.checkEnvContract(env)
	return renderEnvBrief(env, warnings)
}

// cpuBoundConcurrencyCap 是注入提示词的 CPU 密集命令并发上限：取逻辑核数与 4 的
// 较小值。在本机（2 核 4 线程）就是 4；大机器上也不鼓励 agent 把全部核压满。
func cpuBoundConcurrencyCap() int {
	if n := runtime.NumCPU(); n < 4 {
		return n
	}
	return 4
}

// renderEnvBrief is the compact Chinese block appended to planner/worker system
// prompts (#19). Includes only what changes an action: platform+executor, shells,
// working dirs, probed tools, contract warnings.
func renderEnvBrief(env envSnapshot, warnings []string) string {
	var b strings.Builder
	b.WriteString("【共享环境上下文（本任务全部 agent 共用；平台准入时探测，与实际执行器一致）】：")
	fmt.Fprintf(&b, "\n- 操作系统：%s/%s；Bash 工具执行器：%s", env.OS, env.Arch, env.Executor)
	if len(env.Shells) > 0 {
		fmt.Fprintf(&b, "；PATH 上可用 shell：%s", strings.Join(env.Shells, "、"))
	}
	if env.WorkDir != "" {
		fmt.Fprintf(&b, "\n- 任务工作目录：%s（默认 CWD）；数据目录：%s", env.WorkDir, env.DataDir)
	}
	if len(env.Tools) > 0 {
		parts := make([]string, 0, len(env.Tools))
		for _, tool := range env.Tools {
			if tool.Version == "" {
				parts = append(parts, tool.Name)
			} else {
				parts = append(parts, tool.Name+"（"+tool.Version+"）")
			}
		}
		fmt.Fprintf(&b, "\n- PATH 上探测到的工具：%s", strings.Join(parts, "；"))
	}
	if env.Executor != "bash" {
		fmt.Fprintf(&b, "\n- 命令按 %s 语法书写（不要用 Unix 专属命令/写法）。", env.Executor)
	}
	// CPU 治理：任务里的穷举/爆破/大规模 fuzz 脚本曾把本机 CPU 打满，连带平台服务与
	// SQLite 写入一起变慢。这里给出可执行的硬约束，避免模型写出无上限的并发脚本。
	fmt.Fprintf(&b, "\n- 本机逻辑 CPU %d 核。CPU 密集型命令（穷举、爆破、大规模 fuzz）：自限并发（进程/线程数 ≤ %d，"+
		"禁止无上限 multiprocessing），预计超过 1 分钟的命令用 run_in_background 后台执行、不再需要时立即 TaskStop；"+
		"避免多个 worker 同时跑满 CPU——平台服务与数据库会被拖慢。", runtime.NumCPU(), cpuBoundConcurrencyCap())
	if len(warnings) > 0 {
		fmt.Fprintf(&b, "\n- ⚠ 环境契约警告：%s", strings.Join(warnings, "；"))
	}
	return b.String()
}
