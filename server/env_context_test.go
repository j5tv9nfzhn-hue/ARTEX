package server

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestRenderEnvBriefCarriesCPUBudget 守护 CPU 治理约束在位：环境简报会注入全部
// planner/worker 的 system prompt，是「穷举/爆破/大规模 fuzz 自限并发、长命令走后台」
// 唯一能随任务到达模型的通道。删掉这一行会让 CPU 饱和问题原样回归。
func TestRenderEnvBriefCarriesCPUBudget(t *testing.T) {
	env := envSnapshot{
		OS:       "windows",
		Arch:     "amd64",
		Executor: "powershell",
		Shells:   []string{"powershell", "cmd"},
		WorkDir:  `C:\data\tasks\7\i1`,
		DataDir:  `C:\data`,
		Tools:    []envToolVersion{{Name: "python", Version: "Python 3.13.0"}},
	}
	brief := renderEnvBrief(env, nil)

	for _, want := range []string{
		"本机逻辑 CPU " + strconv.Itoa(runtime.NumCPU()) + " 核",
		"run_in_background",
		"TaskStop",
		"禁止无上限 multiprocessing",
		"进程/线程数 ≤ " + strconv.Itoa(cpuBoundConcurrencyCap()),
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("环境简报缺少 %q：\n%s", want, brief)
		}
	}
	// 既有内容不能被顶掉：平台/执行器与工作目录仍在。
	for _, want := range []string{"Bash 工具执行器：powershell", `C:\data\tasks\7\i1`} {
		if !strings.Contains(brief, want) {
			t.Errorf("环境简报原有内容丢失 %q：\n%s", want, brief)
		}
	}
}

// TestCPUBoundConcurrencyCap 约束上限取值：小机器按核数收紧，大机器封顶 4。
func TestCPUBoundConcurrencyCap(t *testing.T) {
	got := cpuBoundConcurrencyCap()
	if got < 1 {
		t.Fatalf("并发上限为 %d，至少应为 1", got)
	}
	if got > 4 {
		t.Fatalf("并发上限为 %d，超过封顶值 4", got)
	}
	if want := runtime.NumCPU(); want < 4 && got != want {
		t.Fatalf("逻辑核 %d 台机器上并发上限应为 %d，实际 %d", want, want, got)
	}
}
