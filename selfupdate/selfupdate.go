// Package selfupdate implements ARTEX-HYDRA 的页面一键更新：从 GitHub Release 拉取新版
// 二进制、校验、暂存，并在下次启动时原子换装。
//
// 整体分工（见 start.sh / start.bat）：
//
//	启动脚本  = 傻瓜守护循环，只负责"进程退出后按退出码决定是否再拉起"
//	本包      = 全部易错逻辑（下载 / SHA256 校验 / 冒烟 / 换装 / 失败回滚）
//
// 之所以把换装放在 Go 而不是脚本里，是因为 sha256 校验和冒烟测试在 sh 和 bat 上
// 要写两套（sha256sum / shasum / certutil），而这恰恰是最不能出错的一环——换上一个
// 跑不起来的二进制，守护进程会忠实地反复拉起它，用户只能上机器手工救。
//
// 一次完整升级经过三次进程启动：
//
//	① 旧版 server 收到 /api/update/apply → 下载校验 → 暂存 artex-rea.new → exit 75
//	② 脚本重新拉起旧版 → Bootstrap 发现 artex-rea.new → 校验+冒烟 → 换装 → exit 75
//	③ 脚本重新拉起，此时已是新版 → Bootstrap 记一次尝试 → 启动成功后清除标记
//
// 任何一步失败都退回旧版：② 校验不过就删掉暂存件继续跑旧版；③ 连续 3 次没活到
// 清除标记（起不来就崩）则自动把 artex-rea.old 换回去。
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart 是"请守护进程重新拉起我"的退出码（EX_TEMPFAIL）。启动脚本看到它
// 就立刻重跑，不计入崩溃退避。0 表示用户正常停止（脚本退出循环），其余均视为崩溃。
const ExitRestart = 75

// maxAttempts 是换装后允许的启动尝试次数。新版每次启动都会把计数 +1，活过
// settleDelay 则清除标记；连崩 maxAttempts 次说明新版根本起不来，自动回滚。
const maxAttempts = 3

// Paths 是一次升级涉及的全部文件，统一挂在**可执行文件所在目录**下。
// 刻意不用 CWD：服务化运行时工作目录可能是 / 或任意路径，用 CWD 会让暂存件落到
// 别处，换装逻辑直接失效。
type Paths struct {
	Dir     string // 可执行文件所在目录
	Current string // 当前运行的二进制        artex-rea      / artex-rea.exe
	New     string // 暂存的新版本            artex-rea.new  / artex-rea.new.exe
	Sum     string // 新版本的 sha256（hex）  artex-rea.new.sha256 / artex-rea.new.exe.sha256
	Old     string // 换装前备份的旧版本      artex-rea.old  / artex-rea.old.exe
	Marker  string // 升级状态标记            artex-rea.upgrade.json
}

// ResolvePaths 按当前可执行文件推导全部升级路径。
//
// Windows 上 .new/.old 也必须带 .exe 后缀，否则冒烟测试和换装后的执行都会失败，
// 所以先把后缀摘掉再拼，两个平台的命名才对称。
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("无法确定可执行文件位置：%w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return pathsFor(exe), nil
}

// pathsFor 按给定的可执行文件路径推导旧协议文件（.new/.old/marker）。
//
// 单独抽出来是因为迁移到版本化布局后进程实际跑在 versions/<ver>/ 里，
// 而旧协议的文件全都留在安装根目录——那种情况下要按根目录里的入口程序重新推导，
// 不能拿 os.Executable()（它指向版本目录）。
func pathsFor(exe string) Paths {
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // Windows 上是 ".exe"，Unix 上通常为空
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}
}

// marker 记录一次换装的进度，用来在新版起不来时触发自动回滚。
type marker struct {
	From     string `json:"from"`     // 升级前的版本
	To       string `json:"to"`       // 目标版本
	Attempts int    `json:"attempts"` // 换装后已尝试启动的次数
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged 清掉暂存件。换装成功、校验失败、用户取消都走它，避免残留的
// artex.new 在下次启动时被重新尝试。
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// versionParts 是版本号参与比较的段数。
//
// 前三段是常规语义版本，第四段是「同一个小版本重发的微修订」：修掉一个只影响
// 升级路径的 bug、不值得占一个 patch 位时，发 2.4.0.1 而不是 2.4.1。缺省段按 0
// 补齐，所以 2.4.0 与 2.4.0.0 相等——老版本号不需要回填第四位。
const versionParts = 4

// CompareVersions 比较两个版本号，返回 -1/0/1（a<b / a==b / a>b）。
// ok=false 表示至少一边不是可比较的版本号（例如本地开发构建的 "dev" 或
// git describe 产出的 "0.3.7-2-gabc1234-dirty"），此时调用方应禁用一键更新，
// 否则会把开发中的构建"升级"成正式版、覆盖掉未提交的改动。
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range versionParts {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion 解析 "v0.3.7" / "0.3.7" / "0.3.7.1" 形式的版本号为 [4]int。
//
// 接受 3 段或 4 段，每一段都必须是纯数字（第四段缺省补 0）。多出来的第五段、
// 任何后缀、空段、带正负号的数字一律判为不可比较。
//
// 严格的原因：build.sh 在非 tag 构建时用 git describe 产出
// "0.3.7-2-gabc1234" 这类带后缀的版本，它们必须被判为不可比较，而不是被当成
// 0.3.7 —— 否则开发构建会被误判为"已是最新"或被正式版覆盖。
func parseVersion(s string) ([4]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [4]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 && len(parts) != 4 {
		return [4]int{}, false
	}
	var out [4]int
	for i, p := range parts {
		// 只认 ASCII 数字：strconv.Atoi 会放过 "+1"、"-0" 这类写法，而它们
		// 不该被当成合法版本段（"-0" 甚至会让 Atoi 返回 0 而不报错）。
		if p == "" {
			return [4]int{}, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return [4]int{}, false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [4]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// supervisedEnv 由 start.sh / start.bat 设置：它告诉进程"外面有个循环会在
// 我以 ExitRestart 退出后把我重新拉起来"。没有它，换装后进程退出就再没人拉起，
// 用户看到的是"更新完程序打不开了"。
const supervisedEnv = "ARTEX_SUPERVISED"

// Supervised 报告当前进程是否由守护脚本（start.sh / start.bat）启动。
func Supervised() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(supervisedEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// StagedVersion 返回已下载、已校验、等待下次启动生效的版本号；没有暂存件时返回空串。
//
// 自动更新只做到这一步就停：把包下好、校验好放在磁盘上，换装留给用户下一次
// 启动。这样自动更新永远不会在渗透任务跑到一半时把进程重启掉。
func StagedVersion() string {
	// 版本化布局：台账里"已暂存 / 已安装待重启"的目标版本就是答案。
	// 这条必须先于旧协议判断，否则新流程下前端永远显示"没有待生效的版本"，
	// 用户会以为自动更新什么都没做。
	if l, lerr := ResolveLayout(); lerr == nil {
		if j, ok := ReadJournal(l); ok {
			switch j.State {
			case StateStaged, StateApplying, StateSwapped:
				if v := strings.TrimSpace(j.To); v != "" {
					return v
				}
			}
		}
	}
	// 旧协议兼容：marker + artex-rea.new（v2.2.2 及更早的暂存方式）。
	p, err := ResolvePaths()
	if err != nil {
		return ""
	}
	mk, ok := readMarker(p.Marker)
	if !ok || strings.TrimSpace(mk.To) == "" {
		return ""
	}
	if _, err := os.Stat(p.New); err != nil {
		return ""
	}
	return mk.To
}

// InDocker 报告进程是否跑在容器里。Docker 下换装写的是容器可写层，
// `docker compose up -d` 重建容器会退回镜像自带的版本——这是预期行为
// （那时用户本来就在拉新镜像），但前端要能据此把话说清楚。
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
