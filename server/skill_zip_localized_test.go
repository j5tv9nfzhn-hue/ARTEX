package server

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
)

// TestSkillZipErrorsLocalized 验证 Skill 上传错误与压缩方法标签使用中文。
// 技术方法名和原始诊断保留，项目自有提示不得残留韩文。
func TestSkillZipErrorsLocalized(t *testing.T) {
	// Message constants rendered with sample arguments.
	assertChineseError(t, "errSkillZipParse",
		fmt.Errorf(errSkillZipParse, fmt.Errorf("boom")).Error())
	assertChineseError(t, "errSkillZipEncrypted",
		fmt.Sprintf(errSkillZipEncrypted, "demo/SKILL.md"))
	assertChineseError(t, "errSkillZipUnsupported",
		fmt.Sprintf(errSkillZipUnsupported, "LZMA", 14, "demo/SKILL.md"))

	// A non-zip upload must reach the user as a Simplified Chinese hint, not a raw stdlib error.
	if _, err := newSkillZipReader([]byte("this is not a zip archive")); err == nil {
		t.Fatal("非 zip 输入应返回错误")
	} else {
		assertChineseError(t, "newSkillZipReader", err.Error())
		if !strings.Contains(err.Error(), "压缩文件") {
			t.Fatalf("parse error = %q，预期包含“压缩文件”提示", err.Error())
		}
	}

	// 压缩方法名不得残留韩文；AES 与未知方法回退使用中文标签。
	for m, name := range zipMethodNames {
		for _, r := range name {
			if unicode.Is(unicode.Hangul, r) {
				t.Fatalf("zipMethodNames[%d] = %q 仍有韩文残留", m, name)
			}
		}
	}
	assertChineseError(t, "zipMethodName(AES)", zipMethodName(zipMethodAES))
	assertChineseError(t, "zipMethodName(unknown)", zipMethodName(0xffff))
}
