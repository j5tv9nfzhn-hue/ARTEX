//go:build windows

// 进程树收口：启动时把本进程交给一个「最后一个句柄关闭即终止全部成员」的 Windows
// Job Object，让 ARTEX 派生出去的所有子进程（含孙进程）与 ARTEX 同生共死。
//
// 为什么必须有这道闸：
//   - Bash 工具的每条命令都是 powershell.exe 再拉起真正的程序（python、rg、npx…），
//     python 的 multiprocessing / ThreadPoolExecutor 还会再派一层。这些进程只在
//     「父进程还持有 ctx 并愿意 kill 树」时才会被收走；它们不会跟随父进程死亡。
//   - ARTEX 有三条不优雅的退出路径：控制台关闭 / 任务管理器强杀、进程崩溃、以及
//     boot_windows.go 里新实例 TerminateProcess 掉持端口的老实例（自更新换装、双击
//     二次启动都会走到）。任何一条发生，正在执行的穷举/爆破/fuzz 脚本都会变成孤儿，
//     继续占满 CPU（而且继续打目标），直到人工去任务管理器里清理。
//   - Job Object 是系统给的答案：本进程进入 Job 后，之后 CreateProcess 出来的进程
//     全部自动继承 Job；本进程一终止（正常退出、强杀、崩溃都一样），最后一个句柄
//     随进程关闭，内核终止 Job 内所有残余进程。
//
// 失败不致命：CI/沙箱里可能已经处于别的 Job 中（嵌套 Job 需要 Win8+ 且外层允许），
// 此时退回旧行为并留一条日志，绝不阻断启动。

package main

import (
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
)

// guardProcessTree 创建 kill-on-close Job 并把当前进程放进去。
// 句柄故意不关闭：它要活到进程终止那一刻，由内核关闭并连带终止 Job 内的残余进程。
// 失败时返回错误（已记录日志），调用方按「可降级」处理。
func guardProcessTree() error {
	job, err := newKillOnCloseJob()
	if err != nil {
		log.Printf("[boot] 进程树收口未启用：%v；ARTEX 被强杀或崩溃时，正在跑的子进程（python/powershell 等）可能残留", err)
		return err
	}
	if err := assignPIDToJob(job, windows.GetCurrentProcessId()); err != nil {
		log.Printf("[boot] 进程树收口未启用：%v；ARTEX 被强杀或崩溃时，正在跑的子进程（python/powershell 等）可能残留", err)
		_ = windows.CloseHandle(job)
		return err
	}
	log.Printf("[boot] 进程树收口已启用：ARTEX 退出/被强杀/崩溃时，自启的 shell、脚本及其子进程一并终止")
	return nil
}

// jobObjectCpuRateControlInformation 对应 Win32 JOBOBJECT_CPU_RATE_CONTROL_INFORMATION
// (JobObjectInfoClass = 15)：限制整个 Job 进程树（含派生出的 powershell/python 子树）
// 的最大 CPU 占比，始终为宿主机内核、SQLite 写盘与 HTTP 管理服务保留算力余量。
type jobObjectCpuRateControlInformation struct {
	ControlFlags uint32
	CpuRate      uint32
}

const (
	jobObjectLimitPriorityClass           = 0x00000020
	jobObjectLimitDieOnUnhandledException = 0x00000400
	belowNormalPriorityClass              = 0x00004000
	jobObjectInfoClassCpuRateControl      = 15
	jobObjectCpuRateControlEnable         = 0x00000001
	jobObjectCpuRateControlHardCap        = 0x00000004
)

// newKillOnCloseJob 创建「最后一个句柄关闭时终止全部成员」的 Job，并启用异常静默退出、
// BELOW_NORMAL_PRIORITY_CLASS 优先级降级与尽力而为的进程树硬性 CPU 占比上限（50%）。
func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	// BREAKAWAY_OK 是给自更新用的：退出前要 CreateProcess 一个"不属于本 Job"的新进程
		// （见 selfupdate/relaunch_windows.go），否则父进程退出时它会被 Job 一起杀掉。
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
		windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK |
		jobObjectLimitDieOnUnhandledException | jobObjectLimitPriorityClass
	info.BasicLimitInformation.PriorityClass = belowNormalPriorityClass
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		// 某些受限容器不允许设置 PriorityClass，回退为仅设置 KILL_ON_JOB_CLOSE
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
			windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK |
			jobObjectLimitDieOnUnhandledException
		info.BasicLimitInformation.PriorityClass = 0
		if _, err2 := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err2 != nil {
			_ = windows.CloseHandle(job)
			return 0, err2
		}
	}
	applyJobCPURateCap(job, 50)
	return job, nil
}

func applyJobCPURateCap(job windows.Handle, percent uint32) {
	if percent == 0 || percent >= 100 {
		return
	}
	rateInfo := jobObjectCpuRateControlInformation{
		ControlFlags: jobObjectCpuRateControlEnable | jobObjectCpuRateControlHardCap,
		CpuRate:      percent * 100,
	}
	_, _ = windows.SetInformationJobObject(
		job,
		jobObjectInfoClassCpuRateControl,
		uintptr(unsafe.Pointer(&rateInfo)),
		uint32(unsafe.Sizeof(rateInfo)),
	)
}

// assignPIDToJob 把指定 pid 的进程放进 Job。子进程一旦进入 Job，之后派生的进程自动
// 继承，因此只要在目标进程开始派生之前调用即可。
func assignPIDToJob(job windows.Handle, pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h) //nolint:errcheck // 句柄仅用于本次赋值
	return windows.AssignProcessToJobObject(job, h)
}
