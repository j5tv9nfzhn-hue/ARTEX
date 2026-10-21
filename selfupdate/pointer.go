package selfupdate

// 版本指针：谁是"当前版本"，只由这一个文件说了算。
//
// 升级 = 新增 versions/<新版本>/ + 原子改写这个文件；回滚 = 把 Current/Previous 对调。
// 两条路径都不碰任何正在运行的可执行文件，因此在 Windows 上也不会遇到
// 「正在运行的 exe 不能删除/覆盖」这类锁问题。

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Autumn-27/artex/fsutil"
)

// Pointer 是版本指针文件的内容。
type Pointer struct {
	Current   string `json:"current"`            // 当前生效的版本（不带 v）
	Previous  string `json:"previous,omitempty"` // 上一个可回滚的版本（不带 v）
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

// ReadPointer 读取指针；文件不存在或内容非法时 ok=false。
func ReadPointer(l Layout) (Pointer, bool) {
	b, err := os.ReadFile(l.Pointer)
	if err != nil {
		return Pointer{}, false
	}
	var p Pointer
	if json.Unmarshal(b, &p) != nil {
		return Pointer{}, false
	}
	p.Current = strings.TrimSpace(p.Current)
	p.Previous = strings.TrimSpace(p.Previous)
	if p.Current == "" {
		return Pointer{}, false
	}
	return p, true
}

// WritePointer 原子改写指针（同目录临时文件 + rename）。
func WritePointer(l Layout, p Pointer) error {
	p.Current = strings.TrimSpace(strings.TrimPrefix(p.Current, "v"))
	p.Previous = strings.TrimSpace(strings.TrimPrefix(p.Previous, "v"))
	if p.Current == "" {
		return fmt.Errorf("拒绝写入空指针")
	}
	if p.UpdatedAt == 0 {
		p.UpdatedAt = time.Now().Unix()
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := fsutil.WriteFileAtomic(l.Pointer, b, 0o644); err != nil {
		return fmt.Errorf("写入版本指针失败：%w", err)
	}
	return nil
}

// EnsurePointer 在首次以新版运行时建立指针，作为"迁移到版本化布局"的起点。
//
// 只写指针、不搬运任何文件：入口程序本身就是当前版本，下次升级才会把新核心放进
// versions/。这样迁移是零风险的——即使指针写失败，行为也和旧版完全一致。
func EnsurePointer(l Layout, version string) {
	v := versionKey(version)
	if v == "unknown" || v == "dev" {
		return // 开发构建不参与版本化布局
	}
	if _, ok := ReadPointer(l); ok {
		return
	}
	if err := EnsureLayoutDirs(l); err != nil {
		return
	}
	if err := WritePointer(l, Pointer{Current: v}); err == nil {
		logUpdate(l, "已建立版本指针：current=%s（后续升级只会新增 versions/ 并改这个指针）", v)
	}
}

// SwitchPointer 把指针切到 to，并把原来的 current 记为 previous。
func SwitchPointer(l Layout, to string) error {
	cur, has := ReadPointer(l)
	next := Pointer{Current: versionKey(to), UpdatedAt: time.Now().Unix()}
	if has && cur.Current != "" && versionKey(cur.Current) != next.Current {
		next.Previous = versionKey(cur.Current)
	}
	if err := WritePointer(l, next); err != nil {
		return err
	}
	logUpdate(l, "版本指针已切换：current=%s previous=%s", next.Current, next.Previous)
	return nil
}

// RollbackTarget 返回可用于回滚的版本号；没有可用目标时返回空串。
func RollbackTarget(l Layout) string {
	ptr, ok := ReadPointer(l)
	if !ok || ptr.Previous == "" {
		return ""
	}
	if _, err := os.Stat(CorePath(l, ptr.Previous)); err != nil {
		// 目标目录缺失（被清理或安装不完整）：清掉指针里的旧记录，避免反复失败。
		logUpdate(l, "回滚目标 %s 的核心文件不存在，忽略该回滚点", ptr.Previous)
		ptr.Previous = ""
		_ = WritePointer(l, ptr)
		return ""
	}
	return ptr.Previous
}

// RollbackPointer 把指针指回 previous（并把当前版本记为新的 previous，允许再滚回来）。
func RollbackPointer(l Layout) error {
	target := RollbackTarget(l)
	if target == "" {
		return fmt.Errorf("没有可回滚的版本")
	}
	cur, _ := ReadPointer(l)
	next := Pointer{Current: target, Previous: cur.Current, UpdatedAt: time.Now().Unix()}
	if next.Previous == next.Current {
		next.Previous = ""
	}
	if err := WritePointer(l, next); err != nil {
		return err
	}
	logUpdate(l, "版本指针已回滚：current=%s（原 current=%s 记为 previous）", next.Current, cur.Current)
	return nil
}

// PointerSummary 供 HTTP 层展示，避免把内部结构直接暴露出去。
func PointerSummary(l Layout) map[string]any {
	ptr, ok := ReadPointer(l)
	out := map[string]any{
		"available": false,
		"versions":  InstalledVersions(l),
	}
	if !ok {
		return out
	}
	out["available"] = true
	out["current"] = ptr.Current
	out["previous"] = ptr.Previous
	out["updated_at"] = ptr.UpdatedAt
	return out
}
