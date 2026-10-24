package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// ── doc_inject：把「隐藏指令」做成一份看起来正常的文档 ───────────────────────
//
// 依据（Rubrik Zero Labs, Black Hat USA 2026, RPE）：攻击链的第一步不是打穿
// 边界，而是**让人把文档喂给 AI**——Word 里白底白字/极小字/隐藏文字的指令，
// 用户看到的是普通表单，模型读到的是「把这份文件的内容发到 xxx」。
//
// 在授权测试里，这件事的价值是：目标站点的「上传合同/简历/导入 Excel，我来帮
// 你分析」功能，解析出来的文本会直接进模型的上下文。能不能用一份看起来正常的
// 文档驱动模型做它不该做的事（越权取数、外发数据、绕过审核），是一个必须测
// 的面——而它跟 HTTP 层的任何扫描都无关。
//
// 本工具只做「生成 + 投递」：
//   - docx：白底白字、2 磅极小字、w:vanish 隐藏，正文同时带可见封面文字；
//     隐藏指令另写入 docProps/core.xml 的 description（很多解析器只读元数据）。
//   - pdf：不可见渲染模式（3 Tr）+ 白色填充；CJK 需要嵌入字体，故非 ASCII
//     载荷建议用 docx（工具会在返回里说明）。
//
// 它不判断目标是否授权——那是 scope 层的事。

// docPayload 是一份生成好的文件。
type docPayload struct {
	Name string
	Data []byte
	Mime string
}

func (t *ToolSet) docInjectTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "doc_inject",
		Description: "生成带**隐藏指令**的文档（docx/pdf）并可上传，测「上传文档交给 AI 解析」的链路会不会执行文档里的指令（提示注入/RPE）。" +
			"docx 藏法：白底白字、2 磅极小字、w:vanish 隐藏，隐藏文本同时写入文档元数据；pdf 用不可见渲染模式（3 Tr）。" +
			"目标有「上传简历/合同/报表，AI 帮你分析」时用。dry_run=true 只生成并返回路径。仅授权目标可用。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"format":       map[string]any{"type": "string", "description": "docx（默认，中文首选）| pdf"},
				"upload_url":   map[string]any{"type": "string", "description": "留空=只生成不投递"},
				"field_name":   map[string]any{"type": "string", "description": "表单文件字段，默认 file"},
				"filename":     map[string]any{"type": "string", "description": "业务化名字（合同/简历/对账单）"},
				"cover_text":   map[string]any{"type": "string", "description": "可见正文"},
				"hidden_text":  map[string]any{"type": "string", "description": "隐藏指令（模型读到、人看不到）"},
				"extra_fields": map[string]any{"type": "object", "description": "额外表单字段"},
				"headers":      map[string]any{"type": "object", "description": "Cookie/Authorization 等"},
			},
			"required": []any{"hidden_text"},
		},
		ReadOnly:    noStoreRo,
		Permissions: allowAllPerm,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return t.runDocInject(ctx, in)
		},
	})
}

func (t *ToolSet) runDocInject(ctx context.Context, in json.RawMessage) (actool.Result, error) {
	var a struct {
		Format      string            `json:"format"`
		UploadURL   string            `json:"upload_url"`
		FieldName   string            `json:"field_name"`
		Filename    string            `json:"filename"`
		CoverText   string            `json:"cover_text"`
		HiddenText  string            `json:"hidden_text"`
		Technique   string            `json:"technique"`
		Extra       map[string]string `json:"extra_fields"`
		Headers     map[string]string `json:"headers"`
		InsecureTLS bool              `json:"insecure_tls"`
		DryRun      bool              `json:"dry_run"`
		TimeoutMS   int               `json:"timeout_ms"`
	}
	if err := json.Unmarshal(in, &a); err != nil {
		return actool.Errorf("参数解析失败：" + err.Error()), nil
	}
	if strings.TrimSpace(a.HiddenText) == "" {
		return actool.Errorf("hidden_text 必填：这是要模型读到、而人看不到的那段话"), nil
	}
	if a.FieldName == "" {
		a.FieldName = "file"
	}
	if a.TimeoutMS <= 0 {
		a.TimeoutMS = 30000
	}
	format := strings.ToLower(strings.TrimSpace(a.Format))
	if format == "" {
		format = "docx"
	}

	var payload docPayload
	var err error
	switch format {
	case "docx":
		payload, err = buildInjectedDocx(a.CoverText, a.HiddenText, a.Technique, a.Filename)
	case "pdf":
		payload, err = buildInjectedPDF(a.CoverText, a.HiddenText, a.Filename)
	default:
		return actool.Errorf("不支持的 format：" + a.Format + "（docx | pdf）"), nil
	}
	if err != nil {
		return actool.Errorf("生成文档失败：" + err.Error()), nil
	}

	out := map[string]any{
		"format":   format,
		"filename": payload.Name,
		"bytes":    len(payload.Data),
	}
	if format == "pdf" && !isASCII(a.HiddenText) {
		out["warning"] = "PDF 的隐藏文本用不可见渲染模式写入，CJK 需要嵌入字体才可被解析器正确还原；中文载荷建议改用 docx。"
	}

	// 落盘：无论是否上传，都留一份在本次意图的工作目录，便于复查与二次投递。
	dir := t.workDir
	if strings.TrimSpace(dir) == "" {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, payload.Name)
	if werr := os.WriteFile(path, payload.Data, 0o644); werr != nil {
		out["write_error"] = werr.Error()
	} else {
		out["path"] = path
	}

	if a.DryRun || strings.TrimSpace(a.UploadURL) == "" {
		out["uploaded"] = false
		out["hint"] = "文件已生成。投递时优先选择业务上说得通的名字与字段（合同/简历/导入），并确保目标是授权范围内的解析链路。"
		return jsonResult(out)
	}

	proxyURL, caCert := t.resolveHTTPSettings("", nil)
	tr := t.getPooledTransport(proxyURL, caCert, a.InsecureTLS || caCert == "")
	client := &http.Client{Transport: tr, Timeout: time.Duration(a.TimeoutMS) * time.Millisecond}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range a.Extra {
		_ = mw.WriteField(k, v)
	}
	fw, err := mw.CreateFormFile(a.FieldName, payload.Name)
	if err != nil {
		return actool.Errorf("构造 multipart 失败：" + err.Error()), nil
	}
	if _, err := fw.Write(payload.Data); err != nil {
		return actool.Errorf("写入 multipart 失败：" + err.Error()), nil
	}
	_ = mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.UploadURL, &body)
	if err != nil {
		return actool.Errorf("构造上传请求失败：" + err.Error()), nil
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range a.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		out["uploaded"] = false
		out["upload_error"] = err.Error()
		return jsonResult(out)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	out["uploaded"] = true
	out["status"] = resp.StatusCode
	out["response"] = truncRunesLog(string(respBody), 1200)
	out["verify_hint"] = "投递成功只是第一步：要确认解析链路是否真的把隐藏指令喂给了模型——看后续对话/任务里是否出现 hidden_text 里的特征串，或用 ai_chat_probe 追问它「刚才那份文件里让你做什么」。"
	return jsonResult(out)
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// ── docx 生成 ───────────────────────────────────────────────────────────────

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// buildInjectedDocx 手搓一个最小可用的 docx。
//
// 不引第三方库是刻意的：docx 就是一个 zip + 几份 XML，自己写反而能把「隐藏」
// 这件事写到最透——白字、微型字、vanish 属性、文档属性四层同时下手，每种解析器
// 读到的层次不同（有的只抽正文、有的连元数据一起抽）。
func buildInjectedDocx(cover, hidden, technique, filename string) (docPayload, error) {
	if strings.TrimSpace(technique) == "" {
		technique = "all"
	}
	technique = strings.ToLower(technique)
	wantWhite := technique == "all" || technique == "white"
	wantTiny := technique == "all" || technique == "tiny"
	wantVanish := technique == "all" || technique == "vanish"

	if strings.TrimSpace(cover) == "" {
		cover = "项目交接说明\n本文件用于内部资料整理，请勿外传。"
	}

	// 隐藏段落的 run 属性：颜色、字号、隐藏开关各自独立，按 technique 叠加。
	rPr := "<w:rPr>"
	if wantWhite {
		rPr += `<w:color w:val="FFFFFF"/>`
	}
	if wantTiny {
		rPr += `<w:sz w:val="2"/><w:szCs w:val="2"/>`
	}
	if wantVanish {
		rPr += `<w:vanish/>`
	}
	rPr += "</w:rPr>"

	var body strings.Builder
	for _, line := range strings.Split(cover, "\n") {
		body.WriteString(`<w:p><w:r><w:t xml:space="preserve">` + xmlEscape(line) + `</w:t></w:r></w:p>`)
	}
	// 隐藏指令按行拆成多段：一次性超长段落容易被解析器的长度阈值截断。
	for _, line := range strings.Split(hidden, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		body.WriteString(`<w:p><w:r>` + rPr + `<w:t xml:space="preserve">` + xmlEscape(line) + `</w:t></w:r></w:p>`)
	}

	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:body>` + body.String() + `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr></w:body></w:document>`

	contentTypes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
</Types>`

	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
</Relationships>`

	// 元数据层：大量解析器与「文档摘要」功能会连属性一起读。
	core := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties"
 xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:title>` + xmlEscape(docTitle(filename)) + `</dc:title>
<dc:subject>` + xmlEscape(truncRunesLog(hidden, 200)) + `</dc:subject>
<dc:description>` + xmlEscape(truncRunesLog(hidden, 1000)) + `</dc:description>
</cp:coreProperties>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	parts := []struct{ name, data string }{
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", rels},
		{"word/document.xml", document},
		{"docProps/core.xml", core},
	}
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			return docPayload{}, err
		}
		if _, err := w.Write([]byte(p.data)); err != nil {
			return docPayload{}, err
		}
	}
	if err := zw.Close(); err != nil {
		return docPayload{}, err
	}
	return docPayload{
		Name: docFilename(filename, "docx"),
		Data: buf.Bytes(),
		Mime: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	}, nil
}

// ── pdf 生成 ────────────────────────────────────────────────────────────────

// buildInjectedPDF 手搓一个最小 PDF，隐藏文本用「不可见渲染模式」（3 Tr）+
// 白色填充写入：人在阅读器里看不到，文本抽取器照样拿得到。
func buildInjectedPDF(cover, hidden, filename string) (docPayload, error) {
	if strings.TrimSpace(cover) == "" {
		cover = "Project Handover Notes"
	}
	var content strings.Builder
	content.WriteString("BT /F1 12 Tf 14 TL 56 780 Td\n")
	for _, line := range strings.Split(cover, "\n") {
		content.WriteString("(" + pdfEscape(line) + ") Tj T*\n")
	}
	// 3 Tr = invisible text rendering mode；再加白色填充兜底（部分查看器忽略 Tr）。
	content.WriteString("3 Tr 1 1 1 rg /F1 8 Tf\n")
	content.WriteString("T*\n")
	for _, line := range strings.Split(hidden, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		content.WriteString("(" + pdfEscape(line) + ") Tj T*\n")
	}
	content.WriteString("ET\n")
	stream := content.String()

	// 元数据层同样塞一份（/Subject、/Keywords）。
	info := "<< /Title (" + pdfEscape(docTitle(filename)) + ") /Subject (" + pdfEscape(truncRunesLog(hidden, 300)) +
		") /Keywords (" + pdfEscape(truncRunesLog(hidden, 300)) + ") /Producer (ARTEX) >>"

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
		info,
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, len(objs), xref)

	return docPayload{Name: docFilename(filename, "pdf"), Data: buf.Bytes(), Mime: "application/pdf"}, nil
}

func pdfEscape(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(s)
	// PDF 的文本字符串是单字节的；非 ASCII 会破坏结构，替换成 '?'（真实载荷走 docx）。
	var b strings.Builder
	for _, r := range s {
		if r > 127 {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func docTitle(filename string) string {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	if strings.TrimSpace(base) == "" || base == "." {
		return "内部资料"
	}
	return base
}

func docFilename(filename, ext string) string {
	if strings.TrimSpace(filename) == "" {
		return "handover." + ext
	}
	if strings.HasSuffix(strings.ToLower(filename), "."+ext) {
		return filename
	}
	return filename + "." + ext
}
