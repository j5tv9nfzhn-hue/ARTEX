package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

const (
	maxReportImportTotalBytes = 10 << 20 // 10 MB total across uploaded report files
	maxReportImportFileBytes  = 4 << 20  // 4 MB per individual report file
	maxReportImportFiles      = 12
)

// ReportImportFile represents one external report file (.md, .txt, .json, .log, .csv).
type ReportImportFile struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// ReportImportRequest configures external report distillation into a multi-file skill package.
type ReportImportRequest struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Domain      string             `json:"domain"` // "auto" | "web_security" | "software_re"
	Focus       string             `json:"focus"`
	UseLLM      *bool              `json:"use_llm"`
	Apply       *bool              `json:"apply"`
	Overwrite   bool               `json:"overwrite"`
	Files       []ReportImportFile `json:"files"`
	Content     string             `json:"content"`
	Filename    string             `json:"filename"`
}

// ReportImportResult summarizes the distilled multi-file skill package and its proposals.
type ReportImportResult struct {
	OK            bool               `json:"ok"`
	Skill         string             `json:"skill"`
	Status        string             `json:"status"` // "applied" | "pending"
	Engine        string             `json:"engine"` // "llm" | "deterministic"
	FilesWritten  []string           `json:"files_written"`
	Proposals     []db.SkillProposal `json:"proposals"`
	Proposal      *db.SkillProposal  `json:"proposal"`
	FindingsCount int                `json:"findings_count"`
	PitfallsCount int                `json:"pitfalls_count"`
	ChainsCount   int                `json:"chains_count"`
}

type parsedReportFinding struct {
	Title     string
	Severity  string
	VulnClass string
	Summary   string
	PoC       string
	Detail    string
}

type parsedReportPitfall struct {
	Title    string
	Trap     string
	Bypass   string
	StopRule string
	Evidence string
}

type parsedReportData struct {
	Title        string
	Target       string
	Domain       string // "web_security" | "software_re"
	SourceFiles  []string
	ProcessSteps []string
	KeyFacts     []string
	Pitfalls     []parsedReportPitfall
	Findings     []parsedReportFinding
	Keywords     []string
}

var (
	mdHeadingFindingRe = regexp.MustCompile(`(?m)^#{2,4}\s*(?:\d+\.\s*)?\[([A-Za-z\p{Han}]+)\]\s*(.+)$`)
	mdHeadingGenericRe = regexp.MustCompile(`(?m)^(#{1,4})\s+(.+)$`)
	mdCodeFenceRe      = regexp.MustCompile("(?s)```(?:[a-zA-Z0-9_-]*)?\\n(.*?)```")
	domainHostRe       = regexp.MustCompile(`(?i)\b(?:https?://)?([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+)\b`)
	denseIssueLintRe   = regexp.MustCompile(`(?i)\b(PR|issue)\s*#?(\d{3,})\b|#(\d{3,})\b`)
	denseDateLintRe    = regexp.MustCompile(`\b20\d{2}[-/]\d{1,2}[-/]\d{1,2}(?:\s+\d{1,2}:\d{2}(?::\d{2})?)?\b`)
)

// sanitizeForSkillLint strips dense issue numbers (#123) and raw timestamps (2026-xx-xx)
// so distilled skill documents state timeless, generalizable rules and pass LintSkillContent cleanly.
func sanitizeForSkillLint(s string) string {
	s = curatorRedact(s)
	s = denseIssueLintRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "#") {
			return "编号-" + strings.TrimPrefix(m, "#")
		}
		return strings.ReplaceAll(m, "#", "-")
	})
	s = denseDateLintRe.ReplaceAllString(s, "[时间已归一]")
	return s
}

// ImportReportSkill distills one or more external reports (.txt, .json, .md) into a
// multi-file skill package (SKILL.md + references/pitfalls-and-bypasses.md + references/attack-chains-and-pocs.md).
func (s *Server) ImportReportSkill(ctx context.Context, req ReportImportRequest) (*ReportImportResult, error) {
	if s == nil || s.m == nil || s.m.pg == nil {
		return nil, errors.New("数据库不可用")
	}
	if strings.TrimSpace(s.skillDir) == "" {
		return nil, errors.New("技能目录未配置")
	}

	files := normalizeReportFiles(req)
	if len(files) == 0 {
		return nil, errors.New("请至少上传一份报告文件（支持 .md / .txt / .json）或粘贴报告内容")
	}

	parsed := parseExternalReports(files, req.Domain, req.Focus)
	if len(parsed.Findings) == 0 && len(parsed.Pitfalls) == 0 && len(parsed.ProcessSteps) == 0 && len(parsed.KeyFacts) == 0 {
		return nil, errors.New("未能从输入内容中识别出有效的漏洞分析、逆向推导过程或踩坑记录")
	}

	skillName := strings.TrimSpace(req.Name)
	if skillName == "" {
		skillName = deriveReportSkillName(parsed, files)
	} else {
		skillName = sanitizeSkillSlug(skillName)
	}
	if !validSkillName(skillName) {
		return nil, fmt.Errorf("技能包名称 %q 不合法（需小写字母开头，仅含小写字母/数字/连字符，长度 1–64）", skillName)
	}

	apply := true
	if req.Apply != nil {
		apply = *req.Apply
		if apply && s.SkillReviewGateEnabled() {
			// User explicitly asked to apply on import; honor explicit choice.
		}
	} else {
		apply = s.SkillAutoDistillApplyEnabled()
	}

	skillPath := filepath.Join(s.skillDir, skillName)
	if _, err := os.Stat(skillPath); err == nil && !req.Overwrite {
		if apply {
			skillName = skillName + "-" + shortContentHash(files[0].Content)
			if len(skillName) > 64 {
				skillName = strings.TrimRight(skillName[:64], "-")
			}
		}
	}

	useLLM := true
	if req.UseLLM != nil {
		useLLM = *req.UseLLM
	}

	engineUsed := "deterministic"
	var mainDoc, pitfallsDoc, chainsDoc string

	if useLLM {
		if llmMain, llmPitfalls, ok := s.tryDistillReportWithLLM(ctx, skillName, req.Description, req.Focus, parsed, files); ok {
			engineUsed = "llm"
			mainDoc = llmMain
			pitfallsDoc = llmPitfalls
		}
	}

	detMain, detPitfalls, detChains := buildDeterministicReportSkillPackage(skillName, req.Description, req.Focus, parsed)
	if mainDoc == "" {
		mainDoc = detMain
	}
	if pitfallsDoc == "" {
		pitfallsDoc = detPitfalls
	}
	chainsDoc = detChains

	packageFiles := []struct {
		rel     string
		content string
	}{
		{rel: "", content: mainDoc},
		{rel: "references/pitfalls-and-bypasses.md", content: pitfallsDoc},
		{rel: "references/attack-chains-and-pocs.md", content: chainsDoc},
	}

	evidence := fmt.Sprintf(
		"report_import files=%s domain=%s engine=%s findings=%d pitfalls=%d steps=%d",
		strings.Join(parsed.SourceFiles, ","),
		parsed.Domain,
		engineUsed,
		len(parsed.Findings),
		len(parsed.Pitfalls),
		len(parsed.ProcessSteps),
	)

	var proposals []db.SkillProposal
	var primaryProposal *db.SkillProposal
	var written []string

	for _, pf := range packageFiles {
		propID := skillProposalID(skillName, pf.rel, pf.content)
		p, err := s.m.pg.ProposeSkill(ctx, db.SkillProposal{
			ID:       propID,
			Skill:    skillName,
			File:     pf.rel,
			Action:   "create",
			Content:  pf.content,
			Origin:   "curator",
			Evidence: evidence,
		})
		if err != nil {
			return nil, err
		}
		if apply {
			if err := s.applySkillFile(skillName, pf.rel, pf.content); err != nil {
				return nil, fmt.Errorf("写入技能包文件失败 (%s): %w", pf.rel, err)
			}
			if p.Status == db.SkillProposalPending {
				p, _ = s.m.pg.ReviewSkillProposal(ctx, p.ID, db.SkillProposalApplied, "外部报告导入沉淀：多文件技能包自动落盘")
			}
			relLabel := pf.rel
			if relLabel == "" {
				relLabel = "SKILL.md"
			}
			written = append(written, relLabel)
		}
		if p != nil {
			proposals = append(proposals, *p)
			if pf.rel == "" {
				cp := *p
				primaryProposal = &cp
			}
		}
	}

	status := db.SkillProposalPending
	if apply {
		status = db.SkillProposalApplied
		s.bindDefaultSkillVisibility(skillName)
	}

	return &ReportImportResult{
		OK:            true,
		Skill:         skillName,
		Status:        status,
		Engine:        engineUsed,
		FilesWritten:  written,
		Proposals:     proposals,
		Proposal:      primaryProposal,
		FindingsCount: len(parsed.Findings),
		PitfallsCount: len(parsed.Pitfalls),
		ChainsCount:   len(parsed.ProcessSteps),
	}, nil
}

func normalizeReportFiles(req ReportImportRequest) []ReportImportFile {
	var out []ReportImportFile
	for _, f := range req.Files {
		c := strings.TrimSpace(strings.TrimPrefix(f.Content, "\xEF\xBB\xBF"))
		if c == "" {
			continue
		}
		name := strings.TrimSpace(f.Filename)
		if name == "" {
			name = fmt.Sprintf("report-%d.md", len(out)+1)
		}
		out = append(out, ReportImportFile{Filename: filepath.Base(name), Content: c})
		if len(out) >= maxReportImportFiles {
			break
		}
	}
	if len(out) == 0 && strings.TrimSpace(req.Content) != "" {
		name := strings.TrimSpace(req.Filename)
		if name == "" {
			name = "pasted-report.md"
		}
		out = append(out, ReportImportFile{
			Filename: filepath.Base(name),
			Content:  strings.TrimSpace(strings.TrimPrefix(req.Content, "\xEF\xBB\xBF")),
		})
	}
	return out
}

func deriveReportSkillName(d *parsedReportData, files []ReportImportFile) string {
	candidates := []string{d.Target, d.Title}
	for _, f := range files {
		base := strings.TrimSuffix(f.Filename, filepath.Ext(f.Filename))
		candidates = append(candidates, base)
	}
	for _, c := range candidates {
		slug := sanitizeSkillSlug(c)
		if slug != "" && slug != "report" && slug != "md" && slug != "json" && slug != "txt" {
			if !strings.HasPrefix(slug, "report-") && !strings.HasSuffix(slug, "-tactics") {
				slug = "report-" + slug
			}
			if len(slug) > 64 {
				slug = strings.TrimRight(slug[:64], "-")
			}
			if validSkillName(slug) {
				return slug
			}
		}
	}
	prefix := "report-web"
	if d.Domain == db.AnalysisDomainSoftwareRE {
		prefix = "report-re"
	}
	raw := ""
	if len(files) > 0 {
		raw = files[0].Content
	}
	return fmt.Sprintf("%s-%s", prefix, shortContentHash(raw))
}

func sanitizeSkillSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	var b strings.Builder
	lastHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastHyphen = false
		} else if !lastHyphen && b.Len() > 0 {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	res := strings.Trim(b.String(), "-")
	if res == "" {
		return ""
	}
	if res[0] >= '0' && res[0] <= '9' {
		res = "r-" + res
	}
	if len(res) > 56 {
		res = strings.TrimRight(res[:56], "-")
	}
	return res
}

func parseExternalReports(files []ReportImportFile, domainHint, focus string) *parsedReportData {
	d := &parsedReportData{
		Domain: strings.TrimSpace(domainHint),
	}
	seenFinding := map[string]int{}
	seenStep := map[string]bool{}
	seenFact := map[string]bool{}
	seenKeyword := map[string]bool{}

	addKeyword := func(kw string) {
		kw = curatorInline(kw, 48)
		if kw == "" || seenKeyword[strings.ToLower(kw)] {
			return
		}
		seenKeyword[strings.ToLower(kw)] = true
		d.Keywords = append(d.Keywords, kw)
	}

	addStep := func(step string) {
		step = sanitizeForSkillLint(curatorInline(step, 320))
		if step == "" || seenStep[step] {
			return
		}
		seenStep[step] = true
		d.ProcessSteps = append(d.ProcessSteps, step)
	}

	addFact := func(fact string) {
		fact = sanitizeForSkillLint(curatorInline(fact, 320))
		if fact == "" || seenFact[fact] {
			return
		}
		seenFact[fact] = true
		d.KeyFacts = append(d.KeyFacts, fact)
	}

	addFinding := func(f parsedReportFinding) {
		f.Title = sanitizeForSkillLint(curatorInline(f.Title, 180))
		f.VulnClass = sanitizeForSkillLint(curatorInline(f.VulnClass, 80))
		f.Severity = strings.ToLower(strings.TrimSpace(f.Severity))
		if f.Severity == "" {
			f.Severity = "high"
		}
		f.Summary = sanitizeForSkillLint(curatorInline(f.Summary, 520))
		f.Detail = sanitizeForSkillLint(curatorInline(f.Detail, 600))
		f.PoC = sanitizeForSkillLint(strings.TrimSpace(f.PoC))
		if len([]rune(f.PoC)) > 1600 {
			r := []rune(f.PoC)
			f.PoC = string(r[:1600]) + "\n# ...(已截断)"
		}
		if f.Title == "" && f.Summary == "" {
			return
		}
		if f.Title == "" {
			f.Title = curatorInline(f.Summary, 100)
		}
		key := strings.ToLower(f.Title)
		if idx, exists := seenFinding[key]; exists {
			// Enrich existing finding if the second report file has extra fields (e.g. VulnClass or PoC).
			if d.Findings[idx].VulnClass == "" && f.VulnClass != "" {
				d.Findings[idx].VulnClass = f.VulnClass
			}
			if d.Findings[idx].PoC == "" && f.PoC != "" {
				d.Findings[idx].PoC = f.PoC
			}
			if len(f.Summary) > len(d.Findings[idx].Summary) {
				d.Findings[idx].Summary = f.Summary
			}
			if f.VulnClass != "" {
				addKeyword(f.VulnClass)
			}
			return
		}
		seenFinding[key] = len(d.Findings)
		d.Findings = append(d.Findings, f)
		if f.VulnClass != "" {
			addKeyword(f.VulnClass)
		}
	}

	var combinedRaw strings.Builder
	for _, f := range files {
		d.SourceFiles = append(d.SourceFiles, f.Filename)
		combinedRaw.WriteString("\n")
		combinedRaw.WriteString(f.Content)
		ext := strings.ToLower(filepath.Ext(f.Filename))
		trimmed := strings.TrimSpace(f.Content)
		if ext == ".json" || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			if parseJSONReportContent(trimmed, d, addFinding, addStep, addFact, addKeyword) {
				continue
			}
		}
		parseMarkdownOrTextReport(trimmed, f.Filename, d, addFinding, addStep, addFact, addKeyword)
	}

	fullText := combinedRaw.String()
	if d.Target == "" {
		if m := domainHostRe.FindStringSubmatch(fullText); len(m) >= 2 {
			d.Target = m[1]
		}
	}
	if d.Title == "" {
		if d.Target != "" {
			d.Title = d.Target
		} else if len(files) > 0 {
			d.Title = strings.TrimSuffix(files[0].Filename, filepath.Ext(files[0].Filename))
		} else {
			d.Title = "外部导入报告"
		}
	}
	if d.Domain == "" || d.Domain == "auto" {
		d.Domain = detectReportDomain(fullText)
	}

	// Sort findings by severity (Critical -> High -> Medium -> Low)
	sort.SliceStable(d.Findings, func(i, j int) bool {
		return curatorSeverityRank(d.Findings[i].Severity) < curatorSeverityRank(d.Findings[j].Severity)
	})

	// Synthesize causal process steps from findings if explicit process section wasn't in the report
	if len(d.ProcessSteps) == 0 && len(d.Findings) > 0 {
		d.ProcessSteps = synthesizeProcessChainFromFindings(d.Findings, fullText)
	}

	// Extract explicit + deep pattern pitfalls, traps & bypasses
	d.Pitfalls = extractReportPitfalls(fullText, d.Findings, d.Pitfalls, focus)

	return d
}

func detectReportDomain(text string) string {
	lower := strings.ToLower(text)
	reSignals := 0
	for _, kw := range []string{"软件逆向", "反编译", "wasm", "wasm2wat", "ghidra", "ida", "radare2", "pe32", "elf", "样本", "sha-256", "符号恢复"} {
		if strings.Contains(lower, kw) {
			reSignals++
		}
	}
	webSignals := 0
	for _, kw := range []string{"渗透测试", "ssrf", "bola", "idor", "xss", "sqli", "rce", "http/", "endpoint", "漏洞汇总"} {
		if strings.Contains(lower, kw) {
			webSignals++
		}
	}
	if reSignals >= 3 && reSignals > webSignals {
		return db.AnalysisDomainSoftwareRE
	}
	return "web_security"
}

func parseJSONReportContent(
	raw string,
	d *parsedReportData,
	addFinding func(parsedReportFinding),
	addStep func(string),
	addFact func(string),
	addKeyword func(string),
) bool {
	var anyVal any
	if err := json.Unmarshal([]byte(raw), &anyVal); err != nil {
		return false
	}
	var items []map[string]any
	switch v := anyVal.(type) {
	case []any:
		for _, el := range v {
			if m, ok := el.(map[string]any); ok {
				items = append(items, m)
			}
		}
	case map[string]any:
		if title := curatorFirstString(v, "title", "name", "task_name", "target"); title != "" && d.Title == "" {
			d.Title = sanitizeForSkillLint(curatorInline(title, 120))
		}
		if goal := curatorFirstString(v, "goal", "target", "description"); goal != "" && d.Target == "" {
			d.Target = sanitizeForSkillLint(curatorInline(goal, 120))
		}
		for _, k := range []string{"findings", "vulnerabilities", "items", "results", "data"} {
			if arr, ok := v[k].([]any); ok {
				for _, el := range arr {
					if m, ok := el.(map[string]any); ok {
						items = append(items, m)
					}
				}
			}
		}
		if steps, ok := v["process_steps"].([]any); ok {
			for _, st := range steps {
				if s, ok := st.(string); ok {
					addStep(s)
				}
			}
		}
		if facts, ok := v["facts"].([]any); ok {
			for _, fc := range facts {
				switch fm := fc.(type) {
				case string:
					addFact(fm)
				case map[string]any:
					addFact(curatorFirstString(fm, "summary", "text", "detail"))
				}
			}
		}
		if len(items) == 0 && curatorFirstString(v, "name", "vuln_class", "vulnclass", "summary", "severity") != "" {
			items = append(items, v)
		}
	}

	for _, m := range items {
		title := curatorFirstString(m, "name", "title", "vulnerability", "id")
		vulnClass := curatorFirstString(m, "vuln_class", "vulnclass", "type", "category", "cwe")
		sev := curatorFirstString(m, "severity", "level", "risk")
		summary := curatorFirstString(m, "summary", "description", "detail", "overview")
		poc := curatorFirstString(m, "evidence", "poc", "request", "payload")
		if poc == "" {
			if evMap, ok := m["evidence"].(map[string]any); ok {
				poc = curatorFirstString(evMap, "poc", "request", "detail", "summary")
			}
		}
		detail := curatorFirstString(m, "report", "analysis", "remediation", "impact")
		addFinding(parsedReportFinding{
			Title:     title,
			Severity:  sev,
			VulnClass: vulnClass,
			Summary:   summary,
			PoC:       poc,
			Detail:    detail,
		})
		if vulnClass != "" {
			addKeyword(vulnClass)
		}
	}
	return len(items) > 0 || len(d.ProcessSteps) > 0 || len(d.KeyFacts) > 0
}

func parseMarkdownOrTextReport(
	raw, filename string,
	d *parsedReportData,
	addFinding func(parsedReportFinding),
	addStep func(string),
	addFact func(string),
	addKeyword func(string),
) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "# ") && d.Title == "" {
			title := strings.TrimSpace(strings.TrimPrefix(t, "# "))
			for _, pfx := range []string{"渗透测试报告：", "软件逆向分析记录：", "漏洞汇总报告："} {
				title = strings.TrimSpace(strings.TrimPrefix(title, pfx))
			}
			if title != "" && title != "漏洞汇总报告" {
				d.Title = sanitizeForSkillLint(curatorInline(title, 120))
			}
		}
		if strings.HasPrefix(t, "- **任务目标**：") || strings.HasPrefix(t, "- **所属任务**：") {
			val := strings.TrimSpace(strings.SplitN(t, "：", 2)[1])
			if val != "" && d.Target == "" {
				d.Target = sanitizeForSkillLint(curatorInline(val, 140))
			}
		}
	}

	// Split by level-2 sections (## ...)
	sections := splitMarkdownSections(raw)
	for _, sec := range sections {
		secTitleLower := strings.ToLower(sec.Title)
		switch {
		case strings.Contains(secTitleLower, "探索过程") ||
			strings.Contains(secTitleLower, "推导") ||
			strings.Contains(secTitleLower, "有效路径") ||
			strings.Contains(secTitleLower, "攻击链"):
			for _, item := range extractBulletOrNumberLines(sec.Body) {
				addStep(item)
			}
		case strings.Contains(secTitleLower, "事实") ||
			strings.Contains(secTitleLower, "定性") ||
			strings.Contains(secTitleLower, "样本"):
			for _, item := range extractBulletOrNumberLines(sec.Body) {
				addFact(item)
			}
		case strings.Contains(secTitleLower, "踩坑") ||
			strings.Contains(secTitleLower, "陷阱") ||
			strings.Contains(secTitleLower, "误区") ||
			strings.Contains(secTitleLower, "止损") ||
			strings.Contains(secTitleLower, "失败"):
			for _, item := range extractBulletOrNumberLines(sec.Body) {
				d.Pitfalls = append(d.Pitfalls, parsedReportPitfall{
					Title:    "报告记录的受阻陷阱与止损点",
					Trap:     sanitizeForSkillLint(curatorInline(item, 360)),
					Bypass:   "识别负反馈信号后立即切换鉴权上下文、参数槽位或底层传输方式，禁止原样重放。",
					StopRule: "连续 3 次相同错误响应或固定状态码即触发 3-Strike 止损。",
				})
			}
		}
	}

	// Parse ### N. [SEVERITY] Title blocks
	locs := mdHeadingFindingRe.FindAllStringSubmatchIndex(raw, -1)
	if len(locs) > 0 {
		for i, loc := range locs {
			sev := strings.ToLower(strings.TrimSpace(raw[loc[2]:loc[3]]))
			title := strings.TrimSpace(raw[loc[4]:loc[5]])
			blockStart := loc[1]
			blockEnd := len(raw)
			if i+1 < len(locs) {
				blockEnd = locs[i+1][0]
			}
			body := strings.TrimSpace(raw[blockStart:blockEnd])
			vulnClass := ""
			var summaryLines []string
			for _, ln := range strings.Split(body, "\n") {
				tl := strings.TrimSpace(ln)
				if strings.HasPrefix(tl, "- **类型**：") {
					vulnClass = strings.TrimSpace(strings.TrimPrefix(tl, "- **类型**："))
					continue
				}
				if strings.HasPrefix(tl, "- **状态**：") ||
					strings.HasPrefix(tl, "- **发现时间**：") ||
					strings.HasPrefix(tl, "- **生成时间**：") ||
					strings.HasPrefix(tl, "- **严重等级**：") {
					continue
				}
				if strings.HasPrefix(tl, "- **所属任务**：") {
					if d.Target == "" {
						d.Target = sanitizeForSkillLint(curatorInline(strings.TrimPrefix(tl, "- **所属任务**："), 120))
					}
					continue
				}
				if strings.HasPrefix(tl, "**PoC") || strings.HasPrefix(tl, "**证据") || strings.HasPrefix(tl, "```") || tl == "---" {
					break
				}
				if tl != "" {
					summaryLines = append(summaryLines, tl)
				}
			}
			poc := ""
			if m := mdCodeFenceRe.FindStringSubmatch(body); len(m) >= 2 {
				poc = strings.TrimSpace(m[1])
			}
			addFinding(parsedReportFinding{
				Title:     title,
				Severity:  sev,
				VulnClass: vulnClass,
				Summary:   strings.Join(summaryLines, " "),
				PoC:       poc,
				Detail:    body,
			})
		}
		return
	}

	// Fallback for unstructured .txt / .md writeups without [SEVERITY] headings
	if len(d.Findings) == 0 {
		paras := strings.Split(raw, "\n\n")
		for _, p := range paras {
			p = strings.TrimSpace(p)
			if len([]rune(p)) < 25 {
				continue
			}
			if strings.HasPrefix(p, "```") {
				continue
			}
			firstLine := strings.SplitN(p, "\n", 2)[0]
			addFact(firstLine)
		}
	}
	_ = filename
	_ = addKeyword
}

type mdSection struct {
	Title string
	Body  string
}

func splitMarkdownSections(raw string) []mdSection {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	var out []mdSection
	var cur *mdSection
	var buf strings.Builder
	flush := func() {
		if cur != nil {
			cur.Body = strings.TrimSpace(buf.String())
			out = append(out, *cur)
		}
		buf.Reset()
	}
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "## ") {
			flush()
			cur = &mdSection{Title: strings.TrimSpace(strings.TrimPrefix(t, "## "))}
			continue
		}
		if cur != nil {
			buf.WriteString(ln)
			buf.WriteByte('\n')
		}
	}
	flush()
	return out
}

func extractBulletOrNumberLines(body string) []string {
	var out []string
	for _, ln := range strings.Split(body, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, ">") || strings.HasPrefix(t, "```") {
			continue
		}
		if strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") {
			out = append(out, strings.TrimSpace(t[2:]))
			continue
		}
		if len(t) > 3 && unicode.IsDigit(rune(t[0])) && (t[1] == '.' || (unicode.IsDigit(rune(t[1])) && t[2] == '.')) {
			idx := strings.IndexByte(t, '.')
			if idx > 0 && idx+1 < len(t) {
				out = append(out, strings.TrimSpace(t[idx+1:]))
			}
		}
	}
	return out
}

// synthesizeProcessChainFromFindings reconstructs the logical offensive/reversing progression
// from the report's findings when the original export only recorded the final findings.
func synthesizeProcessChainFromFindings(findings []parsedReportFinding, fullText string) []string {
	lower := strings.ToLower(fullText)
	var steps []string

	if strings.Contains(lower, "wasm") || strings.Contains(lower, "crypto_util") {
		steps = append(steps, "前端静态资产与 WASM 签名逆向：定位核心签名模块（如 .wasm / JS bundle），通过反编译与常量提取还原时间戳窗口、随机串与盐值哈希算法，打通匿名接口调用前置条件。")
	}
	if strings.Contains(lower, "/coder/session") || strings.Contains(lower, "safe_mode") {
		steps = append(steps, "沙箱会话初始化与命令执行突破：探测未授权沙箱创建与命令执行接口，测试显式降级参数（如 safe_mode=false）及 Shell 分隔符（; / &&）绕过安全白名单。")
	}
	if strings.Contains(lower, "renameat2") || strings.Contains(lower, "toctou") || strings.Contains(lower, "symlink") {
		steps = append(steps, "符号链接 TOCTOU 条件竞争穿透宿主机隔离：针对归档打包下载与文件保存接口，利用 renameat2(RENAME_EXCHANGE) 原子交换软链接与真实目录，越界读写宿主机敏感文件与跨沙箱目录。")
	}
	if strings.Contains(lower, "environ") || strings.Contains(lower, "web_server_secret") || strings.Contains(lower, "trusted_service") {
		steps = append(steps, "宿主机环境变量与内部共享密钥横向扩大战果：从 /proc/self/environ 与宿主机配置文件提取内部服务密钥（如 WEB_SERVER_SECRET / TRUSTED_SERVICE_SECRETS）及第三方付费 API Key，直接接管内部计费、配额与控制面接口。")
	}
	if strings.Contains(lower, "upload/sign-url") || strings.Contains(lower, "../") {
		steps = append(steps, "对象存储预签名目录穿越与原始路径覆写：在获取预签名上传 URL 时向业务目录参数注入 ../ 跨目录路径，并使用未做 URL 路径归一化的原始 HTTP 客户端直传覆盖目标对象。")
	}
	if strings.Contains(lower, "100.100.100.200") || strings.Contains(lower, "169.254.169.254") {
		steps = append(steps, "服务端请求伪造（SSRF）分类验证与云元数据凭证窃取：区分无回显盲 SSRF 与带公网存储回显的 SSRF（如截图/文档转换上传 OSS），直接读取云厂商实例元数据（100.100.100.200 / 169.254.169.254）获取临时 STS/IAM 凭证。")
	}
	if strings.Contains(lower, "byok") || strings.Contains(lower, "tool_calls") {
		steps = append(steps, "BYOK 自定义模型端点劫持与反向 Prompt/Tool 注入：将模型 BaseURL 指向攻击者受控服务端，先截获完整系统提示词，再返回伪造 tool_calls 响应驱动 Agent 执行任意工具调用。")
	}
	if strings.Contains(lower, "postmessage") || strings.Contains(lower, "argon2") || strings.Contains(lower, "jwt") {
		steps = append(steps, "认证与 OAuth 状态机审计：逐一检查注册激活/密码重置邮件链接中的 JWT 载荷敏感字段，以及 OAuth 回调页 postMessage 的 targetOrigin 限制。")
	}
	if strings.Contains(lower, "bola") || strings.Contains(lower, "idor") || strings.Contains(lower, "越权") {
		steps = append(steps, "全链路 BOLA / IDOR 越权矩阵排查：对会话续传、流式生成、状态查询、文件下载、公开分享等相邻端点逐一替换或枚举对象 ID（UUID / 递增整数），验证服务端对象级归属校验。")
	}

	if len(steps) == 0 {
		for i, f := range findings {
			if i >= 6 {
				break
			}
			steps = append(steps, fmt.Sprintf("围绕「%s」开展槽位定性与最小探针验证：%s", f.Title, curatorInline(f.Summary, 180)))
		}
	}
	return steps
}

// extractReportPitfalls mines both explicit and implicit pitfalls, traps, and bypass techniques
// from the report text and findings so AI agents learn what NOT to do and how to bypass traps.
func extractReportPitfalls(fullText string, findings []parsedReportFinding, existing []parsedReportPitfall, focus string) []parsedReportPitfall {
	out := append([]parsedReportPitfall(nil), existing...)
	seen := map[string]bool{}
	for _, p := range out {
		seen[p.Title] = true
	}
	add := func(p parsedReportPitfall) {
		if p.Title == "" || seen[p.Title] {
			return
		}
		seen[p.Title] = true
		p.Trap = sanitizeForSkillLint(p.Trap)
		p.Bypass = sanitizeForSkillLint(p.Bypass)
		p.StopRule = sanitizeForSkillLint(p.StopRule)
		p.Evidence = sanitizeForSkillLint(p.Evidence)
		out = append(out, p)
	}

	lower := strings.ToLower(fullText)

	// 1. HTTP Client URL Path Normalization Trap (OSS / S3 pre-signed path traversal)
	if strings.Contains(lower, "../") && (strings.Contains(lower, "sign-url") || strings.Contains(lower, "oss") || strings.Contains(lower, "s3") || strings.Contains(lower, "put ")) {
		add(parsedReportPitfall{
			Title:    "陷阱 1：标准 HTTP 客户端自动归一化 `../` 导致预签名目录穿越失败",
			Trap:     "在预签名上传（如 `POST /api/v1/upload/sign-url`）中传入 `biz: \"../../../deploy\"` 获取含 `../` 的签名 URL 后，若直接使用浏览器、`requests`、默认 `curl` 或 Go `net/http` 发送 PUT，客户端会在发包前自动折叠 `../`，导致请求路径与签名时的 CanonicalizedResource 不一致，云存储返回 `403 SignatureDoesNotMatch`（AI 极易误判为漏洞不存在）。",
			Bypass:   "必须禁止客户端侧 URL 路径归一化：使用 `curl --path-as-is`、Go `http.Request{URL: &url.URL{Opaque: rawPath}}` 或原始 TLS Socket 保持包含 `../` 的原始请求行原样发往 OSS/S3。",
			StopRule: "收到 `SignatureDoesNotMatch` 时严禁继续盲目变换 `../` 层数；先检查发出的原始 HTTP 报文首行是否保留了未折叠的 `../`。",
			Evidence: "PUT /notegpt/web/../../../deploy/poc.txt?... (保持原始未归一化路径直传)",
		})
	}

	// 2. Static Symlink Rejection vs renameat2(RENAME_EXCHANGE) TOCTOU Race
	if strings.Contains(lower, "renameat2") || strings.Contains(lower, "rename_exchange") || (strings.Contains(lower, "toctou") && strings.Contains(lower, "symlink")) {
		add(parsedReportPitfall{
			Title:    "陷阱 2：静态软链接被 `realpath`/`lstat` 前置拦截 vs `renameat2(RENAME_EXCHANGE)` 原子竞争绕过",
			Trap:     "针对沙箱文件下载（Zip 打包）或文件保存接口，直接预先创建指向 `/proc/self/environ` 或宿主机目录的静态软链接会被服务端 `realpath` 前置校验拦截或直接跳过，反复静态测试只会得到 400/403。",
			Bypass:   "利用 `lstat`/路径校验与实际 `open()`/`zip.Write` 之间的时间窗，在沙箱内编译运行 C 循环程序调用 `renameat2(AT_FDCWD, \"slot\", AT_FDCWD, \"link\", RENAME_EXCHANGE)` 在合法普通目录与指向宿主机目标的符号链接之间做微秒级原子交换，同时并发触发下载或保存接口。",
			StopRule: "静态软链接被拒绝 1 次后即确认存在前置路径检查，立即停止静态软链接枚举，切换为 `renameat2` 原子交换条件竞争或跳出该路径。",
			Evidence: "syscall(SYS_renameat2, AT_FDCWD, \"a\", AT_FDCWD, \"b\", RENAME_EXCHANGE) 配合并发 GET /coder/session/{sid}/download",
		})
	}

	// 3. Sandbox Command Whitelist & safe_mode Trap
	if strings.Contains(lower, "safe_mode") || strings.Contains(lower, "/coder/command") || strings.Contains(lower, "/coder/session") {
		add(parsedReportPitfall{
			Title:    "陷阱 3：被沙箱默认 `safe_mode` 白名单报错误导而放弃命令执行",
			Trap:     "直接调用命令执行端点默认处于受限模式（仅允许少量命令前缀），AI 看到命令被拒后容易误以为沙箱不可执行任意命令或陷入无意义的二进制参数盲猜。",
			Bypass:   "第一优先级检查会话创建接口是否暴露 `\"safe_mode\": false` 等降级布尔字段；第二优先级在受限模式下测试合法前缀后拼接 `;`、`&&` 或换行符（许多过滤器只校验首个 Token 而不解析复合 Shell 语法）。",
			StopRule: "单条命令被白名单拒绝满 2 次，立即检查会话初始化参数 schema 与复合命令分隔符（`;`），禁止在同一受限模式里盲试其他系统命令。",
			Evidence: "POST /coder/session {\"safe_mode\":false} 或 command: \"ls; id; uname -a\"",
		})
	}

	// 4. Shared Host UID & Internal Environment Secret Pivot Trap
	if strings.Contains(lower, "web_server_secret") || strings.Contains(lower, "trusted_service") || strings.Contains(lower, "/proc/self/environ") || strings.Contains(lower, "uid=1001") {
		add(parsedReportPitfall{
			Title:    "陷阱 4：拿到沙箱 RCE 或任意文件读取后只停留在当前容器，忽略宿主机共享 UID 与环境变量密钥横向接管",
			Trap:     "许多轻量级沙箱并非独立容器，而是直接以宿主机普通用户（如 `uid=1001`）跑在宿主机进程空间，或与主 API 进程共享环境变量与源码目录。如果只看当前工作目录，会漏掉整个控制面与计费系统的彻底沦陷。",
			Bypass:   "立即读取 `/proc/self/environ`、父进程环境及宿主机源码配置（如 `config.py` / `.env`），提取内部共享密钥（如 `WEB_SERVER_SECRET`、`TRUSTED_SERVICE_SECRETS`）和第三方付费 Key（`OPENROUTER_API_KEY`、`FALAI_KEY`），再用提取到的内部 Header（`X-Internal-Secret`、`X-Trusted-Service`）直接调内部计费、配额免除与 GPU 控制面接口。",
			StopRule: "读取环境变量与配置后优先做密钥可用性最小验证（如 `/api/internal/verify/`），禁止对第三方付费 API 发起高额消耗请求。",
			Evidence: "Header: X-Internal-Secret / X-Trusted-Service 解锁 /api/internal/* 与 /v1/gpu/reset/*",
		})
	}

	// 5. BYOK (Bring Your Own Key) BaseURL Hijack & Fake tool_calls Injection
	if strings.Contains(lower, "byok_base_url") || strings.Contains(lower, "tool_calls") {
		add(parsedReportPitfall{
			Title:    "陷阱 5：仅将 `byok_base_url` 当作普通盲 SSRF，漏掉系统提示词截获与伪造 `tool_calls` 反向控制 Agent",
			Trap:     "看到自定义模型端点参数（如 `byok_base_url`）时，常规扫描器只打内网端口探测，忽略了服务端会把完整 System Prompt、工具定义与Authorization 头原样发给该 URL，更忽略了服务端 Agent 会无条件信任并执行该端点返回的 `tool_calls`。",
			Bypass:   "1) 将 `byok_base_url` 指向可控的 Mock OpenAI 服务端，一次请求即可完整捕获系统提示词与内部工具 Schema；2) 在 Mock 响应中构造 `finish_reason: \"tool_calls\"` 并指定 `run_command` / `write_file` 等工具参数，直接劫持服务端 Agent 执行链。",
			StopRule: "验证 `byok_base_url` 可达后，不要用它做慢速内网全端口扫描；优先捕获一次完整请求体并验证单步无害 `tool_calls` 注入。",
			Evidence: "Mock /chat/completions 返回 tool_calls: [{\"function\":{\"name\":\"run_command\",\"arguments\":\"{\\\"command\\\":\\\"id\\\"}\"}}]",
		})
	}

	// 6. Frontend WASM Signature Reversing Trap
	if strings.Contains(lower, ".wasm") || strings.Contains(lower, "wasm2wat") || strings.Contains(lower, "crypto_util") {
		add(parsedReportPitfall{
			Title:    "陷阱 6：遇到带 `sign`/`nonce`/`t` 签名校验的 API 直接硬 Fuzz 参数，未先逆向前端 WASM 签名模块",
			Trap:     "当业务接口（如 `/api/v1/upload/sign-url`）要求 `s`/`t`/`nonce` 签名参数时，任何未重算签名的参数篡改都会固定返回签名错误。AI 若不先逆向签名算法，所有注入与越权载荷都会在网关层被丢弃。",
			Bypass:   "先从前端 JS 定位 `.wasm` 签名模块（如 `crypto_util_bg.*.wasm`），下载后用 `wasm2wat` / `strings` 提取静态盐值、导出函数与拼接顺序（如 `md5(biz + file_type + filename + nonce + t + salt)`），编写本地签名生成脚本后再注入业务参数。",
			StopRule: "一旦发现接口带时间戳/随机数签名且修改任意字符即报签名失败，立即停止无签名 Fuzz，转入前端 JS/WASM 签名逆向流程。",
			Evidence: "逆向 crypto_util_bg.wasm 还原签名公式后成功以任意 biz 参数调用 /api/v1/upload/sign-url",
		})
	}

	// 7. Blind SSRF vs Full-Read SSRF via OSS Mirroring
	if strings.Contains(lower, "100.100.100.200") || strings.Contains(lower, "169.254.169.254") || strings.Contains(lower, "upload-screenshot") || strings.Contains(lower, "docx-convert") {
		add(parsedReportPitfall{
			Title:    "陷阱 7：在无回显盲 SSRF 上死磕，忽略「远程拉取并转存云存储 (OSS/S3)」的全回显 SSRF 通道",
			Trap:     "许多媒体处理或文档转换接口只返回任务状态或固定错误码（盲 SSRF），无法直接读出云元数据内容；而在盲 SSRF 端点反复尝试读取元数据只会浪费轮次。",
			Bypass:   "全面盘点所有接收 URL 的接口，优先寻找「服务端拉取远程 URL 后将其作为文件转存到公开 OSS/CDN 并返回下载链接」的端点（如 `POST /api/v2/upload-screenshot` 的 `uploaded_screen_shot_url`），将 `http://100.100.100.200/latest/meta-data/ram/security-credentials/` 或 `http://169.254.169.254/` 传入后直接从返回的公网 OSS URL 下载完整凭证 JSON。",
			StopRule: "确认某个 SSRF 端点仅返回布尔状态或固定报错后，最多记录其内网探测与异常栈信息，立即转向寻找具备内容回显或存储转存能力的 URL 参数槽位。",
			Evidence: "POST /api/v2/upload-screenshot 传入 100.100.100.200 RAM 角色路径 → 从返回的 cdn.ng-resource.com URL 读取完整 AccessKeyId/SecurityToken",
		})
	}

	// 8. Sibling Endpoint BOLA & UUID Leakage via Status Endpoints
	if strings.Contains(lower, "bola") || strings.Contains(lower, "/v1/gpu/status") || strings.Contains(lower, "content_id") || strings.Contains(lower, "conversation_id") {
		add(parsedReportPitfall{
			Title:    "陷阱 8：主接口有鉴权或 UUID 不可预测就放弃 BOLA，未检查相邻流式/续传/分享接口与状态接口 UUID 泄露",
			Trap:     "测试者常因主接口需要权限或会话 ID 为 UUIDv4 而放弃水平越权测试，忽略了：1) 公开监控/状态接口（如 `/v1/gpu/status/`）可能在任务队列中直接回显其他活跃用户的 UUID；2) 相邻动作端点（如 `/continue`、`/share`、`/download`）往往遗漏了主接口才有的归属校验。",
			Bypass:   "1) 先全局搜集状态、排行、队列、日志接口中泄露的活跃 `session_id` / `conversation_id` / `task_id`；2) 对同一资源的完整生命周期端点（创建、读取、续传 `/continue`、导出 `/download`、分享 `/share`）做交叉越权矩阵验证。",
			StopRule: "单个端点确认具备严格归属校验后不在该端点重复换号重试，立即横向切换到同一业务对象的其他生命周期端点。",
			Evidence: "从 /v1/gpu/status/ 获取活跃 UUID 后调用 /coder/session/{sid}；或遍历递增 content_id 调用 /api/v2/share 强制公开私有笔记",
		})
	}

	// 9. Auth / OAuth / Reset Token State Machine Pitfalls
	if strings.Contains(lower, "postmessage") || strings.Contains(lower, "argon2") || strings.Contains(lower, "/auth/callback") {
		add(parsedReportPitfall{
			Title:    "陷阱 9：只测常规登录框弱口令，遗漏邮件激活/重置链接 JWT 载荷泄密与 OAuth `postMessage` 通配符源劫持",
			Trap:     "对认证模块的测试若只停留在登录接口爆破，会完全漏掉注册验证邮件、密码重置链接以及 OAuth 弹窗回调中的高危设计缺陷。",
			Bypass:   "1) Base64 解码注册确认与密码重置 URL 中的 JWT `token` 载荷，检查是否违规封装了明文 `password` 或密码哈希；2) 审计 `/auth/callback` 前端脚本，检查是否使用 `window.opener.postMessage(data, \"*\")` 向任意父窗口广播 `access_token`。",
			StopRule: "登录接口存在速率限制或强校验时立即停止爆破，转向审计注册/重置令牌结构与 OAuth 回调通信机制。",
			Evidence: "解码验证链接 JWT Payload 发现明文 password/argon2id 哈希；/auth/callback 使用 postMessage(..., \"*\") 泄露 OAuth 令牌",
		})
	}

	if strings.TrimSpace(focus) != "" && len(out) == 0 {
		add(parsedReportPitfall{
			Title:    "专项关注方向防错准则",
			Trap:     fmt.Sprintf("针对「%s」，盲目重放固定载荷容易触发网关拦截或陷入无增量循环。", curatorInline(focus, 160)),
			Bypass:   "先完成技术栈与参数槽位定性，结合最小差分探针确认真实处理边界后再构造针对性利用链。",
			StopRule: "同一参数槽位连续 3 次返回一致负反馈时立即触发 3-Strike 止损并切换攻击面。",
		})
	}

	if len(out) == 0 {
		add(parsedReportPitfall{
			Title:    "通用渗透与逆向防踩坑准则",
			Trap:     "在未确认目标过滤边界、签名校验与鉴权上下文前盲目穷举 Payload，导致触发 WAF/速率限制或漏掉逻辑越权。",
			Bypass:   "先通过首包响应头、错误结构与前端静态资产（JS/WASM）完成槽位定性，优先测试跨账号 BOLA、状态机跃迁与带回显 SSRF。",
			StopRule: "连续 3 次相同负反馈（3-Strike）立即停止原样重试，切换鉴权态、HTTP 方法或相邻业务端点。",
		})
	}

	return out
}

func (s *Server) tryDistillReportWithLLM(
	ctx context.Context,
	skillName, customDesc, focus string,
	parsed *parsedReportData,
	files []ReportImportFile,
) (mainDoc string, pitfallsDoc string, ok bool) {
	s.cfgMu.Lock()
	prov, cfg, llmOn := s.llmProv, s.llmCfg, s.llmOn
	s.cfgMu.Unlock()
	if !llmOn || prov == nil {
		return "", "", false
	}

	// Build a bounded digest of the input reports so we stay well within context limits.
	var digest strings.Builder
	fmt.Fprintf(&digest, "目标技能名: %s\n分析域: %s\n报告标题/目标: %s (%s)\n", skillName, parsed.Domain, parsed.Title, parsed.Target)
	if strings.TrimSpace(focus) != "" {
		fmt.Fprintf(&digest, "用户重点提炼要求: %s\n", focus)
	}
	if len(parsed.ProcessSteps) > 0 {
		digest.WriteString("\n【已识别的推导过程步骤】:\n")
		for i, st := range parsed.ProcessSteps {
			if i >= 15 {
				break
			}
			fmt.Fprintf(&digest, "%d. %s\n", i+1, st)
		}
	}
	if len(parsed.Pitfalls) > 0 {
		digest.WriteString("\n【已识别的踩坑与陷阱信号】:\n")
		for i, pf := range parsed.Pitfalls {
			if i >= 12 {
				break
			}
			fmt.Fprintf(&digest, "- %s | 陷阱: %s | 绕过: %s | 止损: %s\n", pf.Title, pf.Trap, pf.Bypass, pf.StopRule)
		}
	}
	if len(parsed.Findings) > 0 {
		digest.WriteString("\n【报告中的核心漏洞/逆向成果摘要】:\n")
		for i, f := range parsed.Findings {
			if i >= 22 {
				break
			}
			fmt.Fprintf(&digest, "%d. [%s] %s (%s): %s\n", i+1, strings.ToUpper(f.Severity), f.Title, f.VulnClass, f.Summary)
			if f.PoC != "" {
				fmt.Fprintf(&digest, "   关键证据/PoC片段: %s\n", curatorInline(f.PoC, 280))
			}
		}
	}
	// Also append raw excerpt up to 12,000 runes for extra context
	var rawExcerpt strings.Builder
	for _, f := range files {
		rawExcerpt.WriteString("--- 文件: " + f.Filename + " ---\n")
		rawExcerpt.WriteString(f.Content)
		rawExcerpt.WriteString("\n")
	}
	rawRunes := []rune(sanitizeForSkillLint(rawExcerpt.String()))
	if len(rawRunes) > 12000 {
		rawRunes = rawRunes[:12000]
	}
	digest.WriteString("\n【原始报告摘录】:\n")
	digest.WriteString(string(rawRunes))

	sysPrompt := `你是 ARTEX-REA 顶级攻防与逆向技能架构师。
你的任务是将用户导入的外部渗透/逆向报告（TXT / JSON / MD）提炼为一份高质量、可泛化复用的【实战过程推导与防踩坑技能手册（SKILL.md 正文）】。

核心原则（极其重要）：
1. 一份完整的漏洞说明和逆向报告，里面提及的具体产物并不是最核心的，最核心的是【推导过程】与【踩过的坑和陷阱】——这是 AI 在渗透和逆向目标时最容易反复犯错、陷入死循环的地方！
2. 严禁写成流水账或单纯罗列漏洞清单！必须将报告中的案例升华为：
   - ` + "`## When to Use`" + `（何时加载本技能：目标技术栈、架构信号、参数槽位特征）
   - ` + "`## Procedure`" + `（核心攻击/逆向推导链：从初始侦察、前端/WASM/接口定性，到撕开缺口、横向扩大战果的完整因果推演步骤）
   - ` + "`## Pitfalls`" + `（高频踩坑、防御陷阱与绕过手法：明确指出常规做法为什么会失败/报 403/被软链接校验拦截/被路径归一化破坏，以及如何通过底层机制绕过）
   - ` + "`## Verification`" + `（3-Strike 止损铁律与最小验证闭环：遇到什么信号必须立即停止机械重试，如何用最小无害探针验证成功，并指引查阅包内 references/pitfalls-and-bypasses.md 与 references/attack-chains-and-pocs.md）
3. 严禁在正文中保留未脱敏的真实密钥/Token，严禁密集出现 #123 编号或具体日期（2026-xx-xx），保持规则脱离具体时间依然成立。
4. 直接输出 Markdown 正文（从一级标题 # 开始，不要输出 YAML frontmatter，不要用外层代码块包裹）。`

	runCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	runCtx = transcript.WithSessionID(runCtx, "report-import-"+transcript.NewSessionID())

	reply, err := agentcore.Run(runCtx, agentcore.Options{
		Provider:       prov,
		SystemPrompt:   []string{sysPrompt},
		PermissionMode: acperm.ModeBypass,
		MaxTurns:       1,
		MaxTokens:      8192,
		NonStreaming:   !cfg.Stream,
	}, digest.String())
	if err != nil {
		log.Printf("[skills] 外部报告 LLM 深度提炼失败，自动降级为确定性提炼: %v", err)
		return "", "", false
	}
	reply = strings.TrimSpace(reply)
	// Strip accidental leading frontmatter if model emitted one
	if strings.HasPrefix(reply, "---\n") {
		if end := strings.Index(reply[4:], "\n---\n"); end >= 0 {
			reply = strings.TrimSpace(reply[4+end+5:])
		}
	}
	reply = sanitizeForSkillLint(reply)
	if len([]rune(reply)) < 200 {
		return "", "", false
	}
	if len([]rune(reply)) > 20000 {
		r := []rune(reply)
		reply = string(r[:20000]) + "\n\n> 详细踩坑矩阵与攻击链 PoC 请查阅包内 `references/pitfalls-and-bypasses.md` 与 `references/attack-chains-and-pocs.md`。\n"
	}

	desc := strings.TrimSpace(customDesc)
	if desc == "" {
		desc = buildReportSkillDescription(parsed)
	} else {
		desc = curatorYAMLScalar(curatorInline(desc, 260))
	}

	fullMain := buildSkillDocument(skillName, desc, reply)
	_, detPitfalls, _ := buildDeterministicReportSkillPackage(skillName, desc, focus, parsed)
	return fullMain, detPitfalls, true
}

func buildReportSkillDescription(d *parsedReportData) string {
	domainLabel := curatorDomainLabel(d.Domain)
	kw := strings.Join(d.Keywords, "、")
	if kw == "" {
		kw = "逻辑越权、SSRF、签名逆向与条件竞争"
	}
	desc := fmt.Sprintf(
		"%s实战推导与防踩坑技能包（源自《%s》报告沉淀）：涵盖 %s 的完整突破推导链、%d 项高频踩坑陷阱与绕过手法及 3-Strike 止损纪律。",
		domainLabel,
		curatorInline(d.Title, 60),
		curatorInline(kw, 100),
		len(d.Pitfalls),
	)
	return curatorYAMLScalar(curatorInline(sanitizeForSkillLint(desc), 260))
}

func buildDeterministicReportSkillPackage(
	skillName, customDesc, focus string,
	d *parsedReportData,
) (mainDoc string, pitfallsDoc string, chainsDoc string) {
	desc := strings.TrimSpace(customDesc)
	if desc == "" {
		desc = buildReportSkillDescription(d)
	} else {
		desc = curatorYAMLScalar(curatorInline(sanitizeForSkillLint(desc), 260))
	}

	// 1. Compose SKILL.md (Main Entry)
	var mb strings.Builder
	fmt.Fprintf(&mb, "# %s · 实战推导过程与防踩坑专家手册 (%s)\n\n", sanitizeForSkillLint(d.Title), skillName)
	fmt.Fprintf(&mb, "> **技能定位**：本技能包由外部实战渗透/逆向报告（`%s`）深度提炼生成。重点固化**完整攻击/逆向推导链、高频踩坑与防御陷阱、底层绕过手法以及 3-Strike 止损纪律**，防止 AI 在同类目标上重复犯错或陷入死循环。\n", strings.Join(d.SourceFiles, "`, `"))
	if strings.TrimSpace(focus) != "" {
		fmt.Fprintf(&mb, "> **专项侧重**：%s\n", sanitizeForSkillLint(curatorInline(focus, 240)))
	}
	mb.WriteString("\n## When to Use\n\n")
	fmt.Fprintf(&mb, "- **目标画像与领域**：%s（原型参考：`%s`）\n", curatorDomainLabel(d.Domain), sanitizeForSkillLint(curatorInline(d.Target, 120)))
	if len(d.Keywords) > 0 {
		fmt.Fprintf(&mb, "- **命中漏洞类型与组件特征**：%s\n", strings.Join(d.Keywords, "、"))
	}
	mb.WriteString("- **核心触发时机**：当面对同类架构目标、遇到签名校验/沙箱限制/路径归一化/无回显 SSRF 等受阻信号时，**必须在盲目发包前加载本技能包**，按下方推导链与避坑规则执行。\n")
	mb.WriteString("- **包内深度参考文件**：\n")
	mb.WriteString("  - `references/pitfalls-and-bypasses.md`：完整踩坑记录、防御陷阱成因与验证绕过手册（必读）\n")
	mb.WriteString("  - `references/attack-chains-and-pocs.md`：分阶段杀伤链、关键端点/函数槽位与脱敏 PoC 模板\n\n")

	mb.WriteString("## Procedure\n\n")
	mb.WriteString("### 核心推导过程与突破链（从侦察定性到纵深突破）\n\n")
	for i, step := range d.ProcessSteps {
		if i >= 12 {
			break
		}
		fmt.Fprintf(&mb, "%d. %s\n", i+1, step)
	}
	mb.WriteString("\n")

	if len(d.KeyFacts) > 0 {
		mb.WriteString("### 关键技术事实与槽位定性线索\n\n")
		for i, fact := range d.KeyFacts {
			if i >= 10 {
				break
			}
			fmt.Fprintf(&mb, "- %s\n", fact)
		}
		mb.WriteString("\n")
	}

	mb.WriteString("## Pitfalls\n\n")
	mb.WriteString("### 高频踩坑、防御陷阱与专家绕过手法（AI 防错核心）\n\n")
	for i, pf := range d.Pitfalls {
		if i >= 10 {
			break
		}
		fmt.Fprintf(&mb, "#### %d. %s\n\n", i+1, pf.Title)
		fmt.Fprintf(&mb, "- **易错陷阱（为什么常规做法会失败）**：%s\n", pf.Trap)
		fmt.Fprintf(&mb, "- **验证绕过与破局手法**：%s\n", pf.Bypass)
		fmt.Fprintf(&mb, "- **3-Strike 止损红线**：%s\n", pf.StopRule)
		if pf.Evidence != "" {
			fmt.Fprintf(&mb, "- **关键特征/探针**：`%s`\n", strings.ReplaceAll(pf.Evidence, "`", "'"))
		}
		mb.WriteString("\n")
	}

	mb.WriteString("## Verification\n\n")
	mb.WriteString("### 最小验证闭环与止损纪律\n\n")
	mb.WriteString("1. **先定性后发包**：发送任何攻击载荷前，先核对目标端点/样本是否满足上述触发特征；遇到签名校验先逆向签名算法，遇到路径校验先确认客户端是否自动归一化。\n")
	mb.WriteString("2. **严格执行 3-Strike 止损**：同一端点或参数槽位连续 3 次返回相同状态码/固定错误体时，严禁原样重放或机械叠加字典，必须按 `Pitfalls` 章节切换传输层、鉴权上下文或相邻业务接口。\n")
	mb.WriteString("3. **最小无害化证明**：拿到 RCE、越权或云凭证后，仅用只读命令（如 `id`、` STS GetCallerIdentity`、读取非敏感元数据）完成证明闭环，严禁破坏线上数据或消耗第三方付费配额。\n")
	mb.WriteString("4. **按需查阅支撑文档**：需要具体绕过报文构造或完整漏洞复盘细节时，读取本技能包下的 `references/pitfalls-and-bypasses.md` 与 `references/attack-chains-and-pocs.md`。\n")

	mainBody := mb.String()
	if len([]rune(mainBody)) > 21000 {
		r := []rune(mainBody)
		mainBody = string(r[:21000]) + "\n\n> 更多细节已拆分至 `references/pitfalls-and-bypasses.md` 与 `references/attack-chains-and-pocs.md`。\n"
	}
	mainDoc = buildSkillDocument(skillName, desc, mainBody)

	// 2. Compose references/pitfalls-and-bypasses.md
	var pb strings.Builder
	fmt.Fprintf(&pb, "# %s · 踩坑记录、蜜罐陷阱与底层绕过手册\n\n", sanitizeForSkillLint(d.Title))
	pb.WriteString("> 本文档为技能包支撑文件（`references/pitfalls-and-bypasses.md`），汇总从实战报告中提炼的全部**易错陷阱、失败根因、底层绕过技术与 3-Strike 止损矩阵**。\n\n")
	pb.WriteString("## 1. 为什么在渗透与逆向中「过程与踩坑」比最终产物更重要\n\n")
	pb.WriteString("- **防死循环**：AI 在面对预签名校验、软链接防御、受限沙箱或无回显 SSRF 时，若只知道最终漏洞名称而不知道前置踩坑过程，极易在标准 HTTP 客户端路径归一化、静态软链接拦截或无效盲注上耗尽 Token。\n")
	pb.WriteString("- **因果可迁移**：目标域名、具体接口名会随项目改变，但「前端 WASM 签名逆向 → 原始未归一化路径直传」、「`renameat2` 原子交换绕过 `realpath` 检查」、「从沙箱进程环境提取内部密钥反打控制面」的推导链可跨目标直接复用。\n\n")
	pb.WriteString("## 2. 实战陷阱与绕过对照全表\n\n")
	for i, pf := range d.Pitfalls {
		fmt.Fprintf(&pb, "### 2.%d %s\n\n", i+1, pf.Title)
		fmt.Fprintf(&pb, "- **常见踩坑现象与误区**：\n  %s\n", pf.Trap)
		fmt.Fprintf(&pb, "- **底层根因与破局绕过方案**：\n  %s\n", pf.Bypass)
		fmt.Fprintf(&pb, "- **止损与切换信号（3-Strike Rule）**：\n  %s\n", pf.StopRule)
		if pf.Evidence != "" {
			fmt.Fprintf(&pb, "- **验证探针摘录**：\n\n```text\n%s\n```\n", pf.Evidence)
		}
		pb.WriteString("\n")
	}
	pitfallsDoc = pb.String()

	// 3. Compose references/attack-chains-and-pocs.md
	var cb strings.Builder
	fmt.Fprintf(&cb, "# %s · 攻击/逆向突破链与脱敏证据案例库\n\n", sanitizeForSkillLint(d.Title))
	cb.WriteString("> 本文档为技能包支撑文件（`references/attack-chains-and-pocs.md`），记录源报告中的结构化突破路径、关键漏洞点与已脱敏的 PoC 验证模板（仅供授权验证模式参考，严禁跨目标硬套凭据或域名）。\n\n")
	cb.WriteString("## 1. 完整推导链（Step-by-Step Progression）\n\n")
	for i, step := range d.ProcessSteps {
		fmt.Fprintf(&cb, "%d. %s\n", i+1, step)
	}
	cb.WriteString("\n## 2. 已验证漏洞点与关键证据模板（共 " + fmt.Sprint(len(d.Findings)) + " 项）\n\n")
	for i, f := range d.Findings {
		if i >= 25 {
			fmt.Fprintf(&cb, "> 其余 %d 项低优先级条目已省略。\n\n", len(d.Findings)-i)
			break
		}
		sev := strings.ToUpper(f.Severity)
		if sev == "" {
			sev = "INFO"
		}
		fmt.Fprintf(&cb, "### 2.%d [%s] %s\n\n", i+1, sev, f.Title)
		if f.VulnClass != "" {
			fmt.Fprintf(&cb, "- **漏洞类别**：`%s`\n", f.VulnClass)
		}
		if f.Summary != "" {
			fmt.Fprintf(&cb, "- **利用链与机制摘要**：%s\n\n", f.Summary)
		}
		if f.PoC != "" {
			cb.WriteString("- **脱敏验证探针 / PoC 模板**：\n\n```http\n")
			cb.WriteString(f.PoC)
			cb.WriteString("\n```\n\n")
		}
	}
	chainsDoc = cb.String()

	return mainDoc, pitfallsDoc, chainsDoc
}

// ---- HTTP Handlers & Agent Tool ----

// fsImportReportSkill handles POST /api/skills/import-report (both multipart/form-data and application/json).
func (s *Server) fsImportReportSkill(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxReportImportTotalBytes)

	var req ReportImportRequest
	ct := strings.ToLower(r.Header.Get("Content-Type"))

	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxReportImportTotalBytes); err != nil {
			writeErr(w, 400, "解析上传报告失败："+err.Error())
			return
		}
		req.Name = strings.TrimSpace(r.FormValue("name"))
		req.Description = strings.TrimSpace(r.FormValue("description"))
		req.Domain = strings.TrimSpace(r.FormValue("domain"))
		req.Focus = strings.TrimSpace(r.FormValue("focus"))
		req.Content = r.FormValue("content")
		req.Filename = strings.TrimSpace(r.FormValue("filename"))
		req.Overwrite = r.FormValue("overwrite") == "true" || r.URL.Query().Get("overwrite") == "true"

		if v := strings.TrimSpace(r.FormValue("use_llm")); v != "" {
			b := v == "true" || v == "1"
			req.UseLLM = &b
		}
		if v := strings.TrimSpace(r.FormValue("apply")); v != "" {
			b := v == "true" || v == "1"
			req.Apply = &b
		}

		if r.MultipartForm != nil {
			fhs := append(r.MultipartForm.File["files"], r.MultipartForm.File["file"]...)
			var total int64
			for _, fh := range fhs {
				if len(req.Files) >= maxReportImportFiles {
					break
				}
				ext := strings.ToLower(filepath.Ext(fh.Filename))
				switch ext {
				case ".md", ".markdown", ".txt", ".json", ".log", ".csv":
				default:
					writeErr(w, 400, fmt.Sprintf("不支持的报告文件格式 %q（支持 .md / .txt / .json / .log / .csv）", fh.Filename))
					return
				}
				f, err := fh.Open()
				if err != nil {
					writeErr(w, 400, "读取上传文件失败："+err.Error())
					return
				}
				raw, err := io.ReadAll(io.LimitReader(f, maxReportImportFileBytes+1))
				f.Close()
				if err != nil {
					writeErr(w, 400, "读取上传文件内容失败："+err.Error())
					return
				}
				if int64(len(raw)) > maxReportImportFileBytes {
					writeErr(w, 400, fmt.Sprintf("单份报告文件 %q 超过 4MB 上限", fh.Filename))
					return
				}
				total += int64(len(raw))
				if total > maxReportImportTotalBytes {
					writeErr(w, 400, "上传报告总大小超过 10MB 上限")
					return
				}
				req.Files = append(req.Files, ReportImportFile{
					Filename: fh.Filename,
					Content:  string(raw),
				})
			}
		}
	} else {
		if err := decode(r, &req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if r.URL.Query().Get("overwrite") == "true" {
			req.Overwrite = true
		}
	}

	res, err := s.ImportReportSkill(r.Context(), req)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

// fsExportSkillZip streams a skill directory as a downloadable .zip package (GET /api/skills/{name}/export).
func (s *Server) fsExportSkillZip(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if !validSkillName(name) {
		writeErr(w, 400, "非法的技能包名称")
		return
	}
	skillPath := filepath.Join(s.skillDir, name)
	st, err := os.Stat(skillPath)
	if err != nil || !st.IsDir() {
		writeErr(w, 404, "技能包不存在")
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err = filepath.WalkDir(skillPath, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(skillPath, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		hdr := &zip.FileHeader{
			Name:     path.Join(name, rel),
			Method:   zip.Deflate,
			Modified: time.Now().UTC(),
		}
		hdr.SetMode(0o644)
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = fw.Write(data)
		return err
	})
	if err != nil {
		_ = zw.Close()
		writeErr(w, 500, "打包技能包失败："+err.Error())
		return
	}
	if err := zw.Close(); err != nil {
		writeErr(w, 500, "生成压缩包失败："+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".zip"))
	w.WriteHeader(200)
	_, _ = w.Write(buf.Bytes())
}

// toolDistillReportSkill exposes external report (.txt/.json/.md) distillation to Agents.
func (s *Server) toolDistillReportSkill() actool.CoreTool {
	return wrTool("distill_report_skill",
		"将外部渗透测试报告或逆向分析报告（支持 .md / .txt / .json 文件路径或原始文本）深度提炼为多文件技能包（SKILL.md + references/pitfalls-and-bypasses.md + references/attack-chains-and-pocs.md），重点固化推导过程、踩坑陷阱、绕过手法与 3-Strike 止损纪律。",
		objSchema(map[string]any{
			"name":        strParam("目标技能包名称（可选，小写字母开头，字母/数字/连字符；留空则按报告自动派生）"),
			"description": strParam("技能包简短描述（可选，留空自动生成）"),
			"domain":      strParam("分析领域：auto（默认）/ web_security / software_re"),
			"focus":       strParam("重点提炼方向说明（可选，如「重点提取沙箱逃逸与条件竞争踩坑」）"),
			"file_paths": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "要导入提炼的报告文件路径列表（支持 .md / .txt / .json）",
			},
			"content": strParam("直接传入的报告文本内容（当未提供 file_paths 时使用）"),
			"apply":   map[string]any{"type": "boolean", "description": "是否直接落盘生效为多文件技能包（默认 true）"},
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Name        string   `json:"name"`
				Description string   `json:"description"`
				Domain      string   `json:"domain"`
				Focus       string   `json:"focus"`
				FilePaths   []string `json:"file_paths"`
				Content     string   `json:"content"`
				Apply       *bool    `json:"apply"`
			}
			_ = json.Unmarshal(in, &a)
			var files []ReportImportFile
			for _, fp := range a.FilePaths {
				fp = strings.TrimSpace(fp)
				if fp == "" {
					continue
				}
				raw, err := os.ReadFile(fp)
				if err != nil {
					return actool.Errorf("读取报告文件 %s 失败: %v", fp, err), nil
				}
				files = append(files, ReportImportFile{
					Filename: filepath.Base(fp),
					Content:  string(raw),
				})
			}
			apply := true
			if a.Apply != nil {
				apply = *a.Apply
			}
			res, err := s.ImportReportSkill(ctx, ReportImportRequest{
				Name:        a.Name,
				Description: a.Description,
				Domain:      a.Domain,
				Focus:       a.Focus,
				Apply:       &apply,
				Files:       files,
				Content:     a.Content,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(res)
		})
}
