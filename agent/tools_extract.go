package agent

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	actool "github.com/Autumn-27/norma/tool"
	"github.com/tidwall/gjson"
	xhtml "golang.org/x/net/html"
)

func (t *ToolSet) extractDataTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "extract_data",
		Description: "原生结构化数据提取、敏感凭证扫描与压缩包免工具检视/解压工具（替代 Python re/json/BeautifulSoup/zipfile/tarfile 脚本）。支持从文本、本地文件（如 JS bundle、HTML、配置文件、.zip/.tar.gz/.jar 备份包）或直接按 exchange_id 从流量库响应体中提取：API 端点与隐藏路由(endpoints/js_endpoints)、敏感凭证与高危泄露(secrets)、JSON 路径查询(json_path，基于 gjson 语法)、自定义正则命名捕获组(regex)、HTML 链接(html_links)、HTML 表单结构及隐藏域(html_forms)、压缩包内容列表(archive_list)与安全解压(archive_extract)。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mode": map[string]any{
					"type":        "string",
					"description": "提取模式：endpoints | js_endpoints | secrets | json_path | regex | html_links | html_forms | archive_list | archive_extract",
				},
				"exchange_id": map[string]any{"type": "string", "description": "直接从流量存储读取指定 exchange_id 的响应报文（与 file_path / text 三选一）"},
				"file_path":   map[string]any{"type": "string", "description": "从工作区文件读取待提取内容或压缩包路径（与 exchange_id / text 三选一）"},
				"text":        map[string]any{"type": "string", "description": "直接传入待提取文本（与 exchange_id / file_path 三选一）"},
				"expression":  map[string]any{"type": "string", "description": "当 mode=json_path 时为 GJSON 路径表达式（如 'data.users.#.id'）；当 mode=regex 时为 Go 正则表达式；当 mode=archive_extract 时为可选文件名过滤子串"},
				"dest_dir":    map[string]any{"type": "string", "description": "mode=archive_extract 时的目标解压目录（默认 '<file_path>_extracted'）"},
				"max_matches": map[string]any{"type": "integer", "description": "最大返回条数，默认 200，最大 1000"},
			},
			"required": []any{"mode"},
		},
		ReadOnly:    noStoreRo,
		Permissions: allowAllPerm,
		Run: func(_ context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Mode       string `json:"mode"`
				ExchangeID string `json:"exchange_id"`
				FilePath   string `json:"file_path"`
				Text       string `json:"text"`
				Expression string `json:"expression"`
				DestDir    string `json:"dest_dir"`
				MaxMatches int    `json:"max_matches"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			mode := strings.ToLower(strings.TrimSpace(a.Mode))
			if mode == "" {
				return actool.Errorf("mode 为必填参数"), nil
			}
			maxMatches := defaultInt(a.MaxMatches, 200, 1, 1000)

			if mode == "archive_list" || mode == "archive_extract" {
				fp := strings.TrimSpace(a.FilePath)
				if fp == "" {
					return actool.Errorf("mode=" + mode + " 时 file_path 为必填参数"), nil
				}
				fullPath := t.resolveLocalPath(fp)
				if mode == "archive_list" {
					items, err := listArchiveEntries(fullPath, maxMatches)
					if err != nil {
						if os.IsNotExist(err) {
							return actool.Errorf(t.localPathError("读取压缩包", fp, fullPath)), nil
						}
						return actool.Errorf("读取压缩包失败: " + err.Error()), nil
					}
					b, _ := json.MarshalIndent(map[string]any{
						"mode":    "archive_list",
						"archive": fullPath,
						"count":   len(items),
						"entries": items,
					}, "", "  ")
					return actool.Text(actool.Capture(tc, string(b))), nil
				}
				dest := strings.TrimSpace(a.DestDir)
				if dest == "" {
					dest = fullPath + "_extracted"
				} else {
					dest = t.resolveLocalPath(dest)
				}
				extracted, err := extractArchiveEntries(fullPath, dest, strings.TrimSpace(a.Expression), maxMatches)
				if err != nil {
					return actool.Errorf("解压压缩包失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(map[string]any{
					"mode":            "archive_extract",
					"archive":         fullPath,
					"dest_dir":        dest,
					"extracted_count": len(extracted),
					"extracted_files": extracted,
				}, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil
			}

			var content string
			var sourceLabel string
			switch {
			case strings.TrimSpace(a.ExchangeID) != "":
				if t == nil || t.trafficStore == nil {
					return actool.Errorf("当前环境未挂载流量存储，无法通过 exchange_id 读取"), nil
				}
				exID := strings.TrimSpace(a.ExchangeID)
				_, respRaw, err := t.trafficStore.Get(exID)
				if err != nil {
					return actool.Errorf("读取 exchange_id 失败: " + err.Error()), nil
				}
				content = extractHTTPBodyText(respRaw)
				sourceLabel = "exchange:" + exID
			case strings.TrimSpace(a.FilePath) != "":
				given := strings.TrimSpace(a.FilePath)
				fp := t.resolveLocalPath(given)
				raw, err := os.ReadFile(fp)
				if err != nil {
					if os.IsNotExist(err) {
						return actool.Errorf(t.localPathError("读取文件", given, fp)), nil
					}
					return actool.Errorf("读取文件失败: " + err.Error()), nil
				}
				content = string(raw)
				sourceLabel = "file:" + fp
			case a.Text != "":
				content = a.Text
				sourceLabel = "inline_text"
			default:
				return actool.Errorf("请至少提供 exchange_id、file_path 或 text 之一"), nil
			}

			out := map[string]any{
				"mode":          mode,
				"source":        sourceLabel,
				"source_length": len(content),
			}

			switch mode {
			case "endpoints", "js_endpoints":
				eps := extractJSEndpoints(content, maxMatches)
				out["count"] = len(eps)
				out["endpoints"] = eps

			case "secrets":
				secs := extractSensitiveSecrets(content, maxMatches)
				out["count"] = len(secs)
				out["secrets"] = secs

			case "json_path":
				expr := strings.TrimSpace(a.Expression)
				if expr == "" {
					return actool.Errorf("mode=json_path 时 expression 为必填参数（如 'data.items.#.id'）"), nil
				}
				res := gjson.Get(content, expr)
				if !res.Exists() {
					out["exists"] = false
					out["value"] = nil
				} else {
					out["exists"] = true
					out["type"] = res.Type.String()
					out["value"] = res.Value()
				}

			case "regex":
				expr := strings.TrimSpace(a.Expression)
				if expr == "" {
					return actool.Errorf("mode=regex 时 expression 为必填参数"), nil
				}
				re, err := regexp.Compile(expr)
				if err != nil {
					if hint := re2RepeatHint(expr); hint != "" {
						return actool.Errorf(hint), nil
					}
					return actool.Errorf("正则表达式编译失败（Go RE2 语法）: " + err.Error() + "。请检查括号/转义/量词，或改用 mode=json_path 等结构化提取。"), nil
				}
				names := re.SubexpNames()
				all := re.FindAllStringSubmatch(content, maxMatches)
				var matches []any
				for _, m := range all {
					if len(m) == 1 {
						matches = append(matches, m[0])
						continue
					}
					hasNamed := false
					namedMap := make(map[string]string)
					for idx := 1; idx < len(m) && idx < len(names); idx++ {
						if names[idx] != "" {
							hasNamed = true
							namedMap[names[idx]] = m[idx]
						}
					}
					if hasNamed {
						namedMap["_full"] = m[0]
						matches = append(matches, namedMap)
					} else {
						matches = append(matches, m[1:])
					}
				}
				out["count"] = len(matches)
				out["matches"] = matches

			case "html_links":
				links := extractHTMLLinks(content, maxMatches)
				out["count"] = len(links)
				out["links"] = links

			case "html_forms":
				forms := extractHTMLForms(content, maxMatches)
				out["count"] = len(forms)
				out["forms"] = forms

			default:
				return actool.Errorf("未知的 extract_data mode: " + mode), nil
			}

			b, _ := json.MarshalIndent(out, "", "  ")
			return actool.Text(actool.Capture(tc, string(b))), nil
		},
	})
}

type archiveEntryInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Dir  bool   `json:"dir,omitempty"`
}

func isZipLikePath(p string) bool {
	lp := strings.ToLower(p)
	return strings.HasSuffix(lp, ".zip") || strings.HasSuffix(lp, ".jar") || strings.HasSuffix(lp, ".war") || strings.HasSuffix(lp, ".apk") || strings.HasSuffix(lp, ".docx") || strings.HasSuffix(lp, ".xlsx")
}

func listArchiveEntries(archivePath string, maxItems int) ([]archiveEntryInfo, error) {
	if isZipLikePath(archivePath) {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		var out []archiveEntryInfo
		for _, f := range zr.File {
			if len(out) >= maxItems {
				break
			}
			out = append(out, archiveEntryInfo{
				Name: f.Name,
				Size: int64(f.UncompressedSize64),
				Dir:  f.FileInfo().IsDir(),
			})
		}
		return out, nil
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	lp := strings.ToLower(archivePath)
	if strings.HasSuffix(lp, ".tar.gz") || strings.HasSuffix(lp, ".tgz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	var out []archiveEntryInfo
	for len(out) < maxItems {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(out, archiveEntryInfo{
			Name: hdr.Name,
			Size: hdr.Size,
			Dir:  hdr.Typeflag == tar.TypeDir,
		})
	}
	return out, nil
}

func safeJoinDest(destRoot, entryName string) (string, error) {
	cleanRoot := filepath.Clean(destRoot)
	cleanEntry := filepath.Clean(filepath.FromSlash(entryName))
	if strings.HasPrefix(cleanEntry, "..") || filepath.IsAbs(cleanEntry) {
		return "", fmt.Errorf("拒绝非法路径穿越条目: %s", entryName)
	}
	target := filepath.Join(cleanRoot, cleanEntry)
	if target != cleanRoot && !strings.HasPrefix(target, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("拒绝越界解压路径: %s", entryName)
	}
	return target, nil
}

func extractArchiveEntries(archivePath, destDir, filterSubstr string, maxFiles int) ([]string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	var extracted []string
	if isZipLikePath(archivePath) {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if len(extracted) >= maxFiles {
				break
			}
			if filterSubstr != "" && !strings.Contains(f.Name, filterSubstr) {
				continue
			}
			target, err := safeJoinDest(destDir, f.Name)
			if err != nil {
				continue
			}
			if f.FileInfo().IsDir() {
				_ = os.MkdirAll(target, 0o755)
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			wf, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				rc.Close()
				continue
			}
			_, _ = io.Copy(wf, io.LimitReader(rc, 32*1024*1024))
			wf.Close()
			rc.Close()
			extracted = append(extracted, target)
		}
		return extracted, nil
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	lp := strings.ToLower(archivePath)
	if strings.HasSuffix(lp, ".tar.gz") || strings.HasSuffix(lp, ".tgz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		r = gz
	}
	tr := tar.NewReader(r)
	for len(extracted) < maxFiles {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filterSubstr != "" && !strings.Contains(hdr.Name, filterSubstr) {
			continue
		}
		target, err := safeJoinDest(destDir, hdr.Name)
		if err != nil {
			continue
		}
		if hdr.Typeflag == tar.TypeDir {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			continue
		}
		wf, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			continue
		}
		_, _ = io.Copy(wf, io.LimitReader(tr, 32*1024*1024))
		wf.Close()
		extracted = append(extracted, target)
	}
	return extracted, nil
}

func extractHTTPBodyText(raw string) string {
	if idx := strings.Index(raw, "\r\n\r\n"); idx >= 0 {
		return raw[idx+4:]
	}
	if idx := strings.Index(raw, "\n\n"); idx >= 0 {
		return raw[idx+2:]
	}
	return raw
}

var (
	jsEndpointFullURLRe = regexp.MustCompile(`(?i)https?://[a-z0-9._\-]+(?::\d+)?/[a-z0-9._\-/&?=:%+#{}]+`)
	jsEndpointPathRe    = regexp.MustCompile(`["'` + "`" + `]((?:/[a-zA-Z0-9_\-.{}]+){2,}(?:\?[a-zA-Z0-9_\-&=${}]+)?)["'` + "`" + `]`)
	jsEndpointAPICallRe = regexp.MustCompile(`(?i)(?:fetch|axios\.(?:get|post|put|delete|patch|request)|\.ajax|url\s*:|path\s*:)\s*\(?\s*["'` + "`" + `]([^"'` + "`" + `\s]{2,160})["'` + "`" + `]`)
)

var staticSkipSuffixes = []string{
	".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".woff", ".woff2", ".ttf", ".eot", ".css", ".less", ".scss", ".mp4", ".mp3", ".webp",
}

func isIgnoredEndpointCandidate(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 || len(s) > 200 {
		return true
	}
	if strings.HasPrefix(s, "//") || strings.Contains(s, "w3.org/") || strings.Contains(s, "schema.org") || strings.Contains(s, "reactjs.org") || strings.Contains(s, "vuejs.org") {
		return true
	}
	ls := strings.ToLower(s)
	for _, ext := range staticSkipSuffixes {
		if strings.HasSuffix(ls, ext) {
			return true
		}
	}
	return false
}

func extractJSEndpoints(content string, maxMatches int) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if isIgnoredEndpointCandidate(s) {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, m := range jsEndpointAPICallRe.FindAllStringSubmatch(content, maxMatches*2) {
		if len(m) > 1 {
			add(m[1])
		}
		if len(out) >= maxMatches {
			break
		}
	}
	for _, m := range jsEndpointPathRe.FindAllStringSubmatch(content, maxMatches*2) {
		if len(m) > 1 {
			add(m[1])
		}
		if len(out) >= maxMatches {
			break
		}
	}
	for _, m := range jsEndpointFullURLRe.FindAllString(content, maxMatches*2) {
		add(m)
		if len(out) >= maxMatches {
			break
		}
	}
	sort.Strings(out)
	if len(out) > maxMatches {
		out = out[:maxMatches]
	}
	return out
}

type secretPattern struct {
	Type string
	Re   *regexp.Regexp
}

var builtinSecretPatterns = []secretPattern{
	{Type: "AWS Access Key", Re: regexp.MustCompile(`\b((?:AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16})\b`)},
	{Type: "Aliyun AccessKey", Re: regexp.MustCompile(`\b(LTAI[a-zA-Z0-9]{12,24})\b`)},
	{Type: "TencentCloud SecretId", Re: regexp.MustCompile(`\b(AKID[a-zA-Z0-9]{16,36})\b`)},
	{Type: "GitHub Token", Re: regexp.MustCompile(`\b((?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{36,255})\b`)},
	{Type: "GitLab PAT", Re: regexp.MustCompile(`\b(glpat-[A-Za-z0-9\-_]{20,})\b`)},
	{Type: "Slack Token", Re: regexp.MustCompile(`\b(xox[baprs]-[0-9a-zA-Z\-]{10,72})\b`)},
	{Type: "Google API Key", Re: regexp.MustCompile(`\b(AIza[0-9A-Za-z\-_]{35})\b`)},
	{Type: "OpenAI / Bearer Key", Re: regexp.MustCompile(`\b(sk-[A-Za-z0-9\-_]{24,})\b`)},
	{Type: "JWT Token", Re: regexp.MustCompile(`\b(eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})\b`)},
	{Type: "Private Key PEM", Re: regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----`)},
	{Type: "JDBC / DB Connection URI", Re: regexp.MustCompile(`(?i)\b((?:jdbc:(?:mysql|postgresql|oracle|sqlserver|h2):|mongodb(?:\+srv)?://|redis://|postgres://|mysql://)[^\s"'<>]+)`)},
	{Type: "Hardcoded Secret Assignment", Re: regexp.MustCompile(`(?i)(?:api[_-]?key|secret[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|app[_-]?secret|password|passwd|pwd)\s*[:=]\s*["']([^"'\s]{6,120})["']`)},
}

func extractSensitiveSecrets(content string, maxMatches int) []map[string]string {
	seen := make(map[string]struct{})
	var out []map[string]string
	for _, sp := range builtinSecretPatterns {
		matches := sp.Re.FindAllStringSubmatch(content, maxMatches)
		for _, m := range matches {
			val := m[0]
			if len(m) > 1 && m[1] != "" {
				val = m[1]
			}
			key := sp.Type + ":" + val
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, map[string]string{
				"type":  sp.Type,
				"match": clipUTF8(val, 240),
			})
			if len(out) >= maxMatches {
				return out
			}
		}
	}
	return out
}

func extractHTMLLinks(content string, maxMatches int) []map[string]string {
	doc, err := xhtml.Parse(strings.NewReader(content))
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []map[string]string
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if len(out) >= maxMatches {
			return
		}
		if n.Type == xhtml.ElementNode {
			tag := strings.ToLower(n.Data)
			for _, attr := range n.Attr {
				k := strings.ToLower(attr.Key)
				if (k == "href" || k == "src" || k == "action" || k == "data-url") && strings.TrimSpace(attr.Val) != "" {
					val := strings.TrimSpace(attr.Val)
					key := tag + ":" + k + ":" + val
					if _, ok := seen[key]; !ok {
						seen[key] = struct{}{}
						out = append(out, map[string]string{
							"tag":  tag,
							"attr": k,
							"url":  val,
						})
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

type htmlFormInput struct {
	Tag   string `json:"tag"`
	Name  string `json:"name,omitempty"`
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
}

type htmlFormSummary struct {
	Action  string          `json:"action"`
	Method  string          `json:"method"`
	Enctype string          `json:"enctype,omitempty"`
	Inputs  []htmlFormInput `json:"inputs,omitempty"`
}

func extractHTMLForms(content string, maxMatches int) []htmlFormSummary {
	doc, err := xhtml.Parse(strings.NewReader(content))
	if err != nil {
		return nil
	}
	var forms []htmlFormSummary
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if len(forms) >= maxMatches {
			return
		}
		if n.Type == xhtml.ElementNode && strings.EqualFold(n.Data, "form") {
			f := htmlFormSummary{Method: "GET"}
			for _, attr := range n.Attr {
				switch strings.ToLower(attr.Key) {
				case "action":
					f.Action = strings.TrimSpace(attr.Val)
				case "method":
					if m := strings.ToUpper(strings.TrimSpace(attr.Val)); m != "" {
						f.Method = m
					}
				case "enctype":
					f.Enctype = strings.TrimSpace(attr.Val)
				}
			}
			var collectInputs func(*xhtml.Node)
			collectInputs = func(cn *xhtml.Node) {
				if cn.Type == xhtml.ElementNode {
					tag := strings.ToLower(cn.Data)
					if tag == "input" || tag == "textarea" || tag == "select" || tag == "button" {
						inp := htmlFormInput{Tag: tag}
						for _, a := range cn.Attr {
							switch strings.ToLower(a.Key) {
							case "name":
								inp.Name = a.Val
							case "type":
								inp.Type = a.Val
							case "value":
								inp.Value = a.Val
							}
						}
						f.Inputs = append(f.Inputs, inp)
					}
				}
				for c := cn.FirstChild; c != nil; c = c.NextSibling {
					collectInputs(c)
				}
			}
			collectInputs(n)
			forms = append(forms, f)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return forms
}
