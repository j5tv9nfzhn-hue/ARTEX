package server

import (
	"os"
	"path/filepath"
	"testing"
)

// 归档包删除准备错误的中文化回归测试，复用 assertChineseError。
// 错误经后台归档 Worker 写入 task_archives.error，直接显示于 TaskArchivesPanel。
// stageTaskArchivePackageDelete 是无数据库文件函数，
// 测试同时创建原文件与 .deleting-N，真实触发矛盾分支。
func TestTaskArchiveStagedAndOriginalCoexistErrorLocalized(t *testing.T) {
	dataDir := t.TempDir()
	archivePath := taskArchivePath(dataDir, 21, "42")
	if err := os.MkdirAll(filepath.Dir(archivePath), archiveDirMode); err != nil {
		t.Fatal(err)
	}
	// 创建归档原文件与删除临时文件共存的矛盾状态。
	if err := os.WriteFile(archivePath, []byte("original"), archiveFileMode); err != nil {
		t.Fatal(err)
	}
	staged := archivePath + ".deleting-21"
	if err := os.WriteFile(staged, []byte("archive"), archiveFileMode); err != nil {
		t.Fatal(err)
	}
	_, moved, err := stageTaskArchivePackageDelete(archivePath, 21)
	if err == nil {
		t.Fatalf("原文件与删除临时文件共存时应返回错误 (moved=%v)", moved)
	}
	if moved {
		t.Fatal("共存矛盾状态下 moved 不应为 true")
	}
	assertChineseError(t, "staged_and_original_coexist", err.Error())
}
