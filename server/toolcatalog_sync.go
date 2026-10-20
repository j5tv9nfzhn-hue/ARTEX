package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sort"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/traffic"
	actool "github.com/Autumn-27/norma/tool"
)

// 工具目录同步（tool catalog sync）。
//
// 背景：内置工具的「说明书」（Description + JSON Schema）在运行时取自 DB 的 tools 表，
// 而 SeedTool 是 ON CONFLICT DO NOTHING —— 首次安装写入后永不更新。于是同一份代码在
// 不同机器上可以给模型看**不同**的工具说明：老库停留在安装当天的措辞，新库才是当前
// 代码的措辞。工具选择/参数质量因此随安装时间漂移，且从界面上完全看不出来。
//
// 之前用「一次性 flag + 白名单」补（refreshBuiltinToolSchemas / refreshOverlapToolDescriptions），
// 问题是：① 只覆盖白名单里的 key；② 刷过一次就再也刷不动，之后改描述必须记得改 flag；
// ③ 有没有生效无法观测。
//
// 这里换成内容寻址的幂等同步：
//   - 每个工具在 settings 里记一个 toolseed:<key> = 上次同步时的「代码指纹」；
//   - 行内容仍等于该指纹 → 说明运维没改过 → 代码变了就自动刷成代码默认；
//   - 行内容已不等于指纹 → 运维在页面上改过 → 永久保留（并记日志，不静默覆盖）；
//   - 首次接管（老库没有指纹）→ 刷成代码默认一次，并先备份到 toolseed_backup:<key>；
//   - 设置 tool_catalog_sync=off 可整体关闭接管。
// 绑定关系（agents）与启用开关（enabled）永远不动，只动模型可见的说明与参数结构。

const (
	toolCatalogSyncSetting = "tool_catalog_sync" // off = 不接管工具说明（只诊断，不写库）
	toolSeedHashPrefix     = "toolseed:"         // settings: 上次同步时的代码指纹
	toolSeedBackupPrefix   = "toolseed_backup:"  // settings: 被覆盖前的旧内容和时间，可人工回滚
)

// codeToolSeeds 汇总「代码里当前版本的」全部内置工具说明，作为同步与诊断的唯一真值源。
// 与各 SeedTool 调用点保持同一集合：内置 agent 工具 + 流量工具 + 编排工具 + 平台工具 +
// 复测/软件分析工具。
func (s *Server) codeToolSeeds() map[string]agent.ToolSeed {
	out := map[string]agent.ToolSeed{}
	add := func(t actool.CoreTool) {
		if t == nil || t.Name() == "" {
			return
		}
		out[t.Name()] = agent.ToolSeed{Key: t.Name(), Desc: t.Description(), Schema: t.InputSchema()}
	}
	for _, sd := range agent.BuiltinToolSeeds() {
		out[sd.Key] = sd
	}
	for _, t := range traffic.SeedToolMetas() {
		add(t)
	}
	for _, t := range s.orchestrationTools() {
		add(t)
	}
	for _, t := range s.platformTools() {
		add(t)
	}
	for _, t := range s.findingRetestTools() {
		add(t)
	}
	for _, t := range s.softwareAnalysisTools() {
		add(t)
	}
	return out
}

// canonicalJSON 把 JSON 归一化（对象键排序），使「同样的结构」得到同样的指纹。
func canonicalJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

func toolSeedJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// toolSeedHash 是「Description + Schema」的内容指纹，用于判定代码侧/库侧是否一致。
func toolSeedHash(desc string, schema []byte) string {
	h := sha256.New()
	h.Write([]byte(desc))
	h.Write([]byte{0})
	h.Write(canonicalJSON(schema))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// catalogFingerprint 是整个工具目录的单一指纹：同样的指纹 = 模型看到的说明书逐字相同。
// 三端对比时只需要比这一个字符串。
func catalogFingerprint(hashes map[string]string) string {
	keys := make([]string, 0, len(hashes))
	for k := range hashes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte("="))
		h.Write([]byte(hashes[k]))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

type toolCatalogEntry struct {
	Key      string `json:"key"`
	Status   string `json:"status"` // in_sync | synced | adopted | kept_local
	CodeHash string `json:"code_hash"`
	RowHash  string `json:"row_hash"`
	Note     string `json:"note,omitempty"`
}

type toolCatalogReport struct {
	CodeFingerprint string             `json:"code_fingerprint"`
	RowFingerprint  string             `json:"row_fingerprint"`
	Drifted         bool               `json:"drifted"`
	SyncEnabled     bool               `json:"sync_enabled"`
	MissingInDB     int                `json:"missing_in_db"`
	Counts          map[string]int     `json:"counts"`
	Items           []toolCatalogEntry `json:"items"`
}

// toolCatalogDrift 计算「代码说明 vs 库内说明」的差异，不写库。给 /api/tools/drift 用，
// 也是三端对齐时唯一需要的证据：code_fingerprint 相同 = 说明书相同。
func (s *Server) toolCatalogDrift() toolCatalogReport {
	rep := toolCatalogReport{Counts: map[string]int{}, Items: []toolCatalogEntry{}}
	pg := s.m.PG()
	if pg == nil {
		return rep
	}
	if v, _, _ := pg.GetSetting(toolCatalogSyncSetting); v != "off" {
		rep.SyncEnabled = true
	}
	seeds := s.codeToolSeeds()
	rows, err := pg.ListTools()
	if err != nil {
		log.Printf("[tools] 工具目录诊断：读取失败 %v", err)
		return rep
	}
	codeHashes := map[string]string{}
	rowHashes := map[string]string{}
	for _, row := range rows {
		if row == nil || !row.System {
			continue
		}
		rowHashes[row.Key] = toolSeedHash(row.Description, row.Schema)
	}
	for key, sd := range seeds {
		codeHashes[key] = toolSeedHash(sd.Desc, toolSeedJSON(sd.Schema))
	}
	for _, row := range rows {
		if row == nil || !row.System {
			continue
		}
		codeHash, ok := codeHashes[row.Key]
		entry := toolCatalogEntry{Key: row.Key, CodeHash: codeHash, RowHash: rowHashes[row.Key]}
		if !ok {
			entry.Status = "kept_local"
			entry.Note = "代码中已无此工具（历史行，保留）"
			rep.Counts["kept_local"]++
			rep.Items = append(rep.Items, entry)
			continue
		}
		stored, _, _ := pg.GetSetting(toolSeedHashPrefix + row.Key)
		switch {
		case entry.RowHash == codeHash:
			entry.Status = "in_sync"
		case stored != "" && stored != entry.RowHash:
			entry.Status = "kept_local"
			entry.Note = "运维在页面上改过，保留本地版本"
		default:
			entry.Status = "drifted"
		}
		rep.Counts[entry.Status]++
		rep.Items = append(rep.Items, entry)
	}
	// 指纹只比较两边都有的 key：老库缺行属于「迁移未跑」，与「说明书被改」是两种病，
	// 混在一起会让三端指纹永远对不上。缺行单独计数并在 items 里标出。
	commonCode := map[string]string{}
	commonRow := map[string]string{}
	for key, ch := range codeHashes {
		if rh, ok := rowHashes[key]; ok {
			commonCode[key] = ch
			commonRow[key] = rh
		} else {
			rep.MissingInDB++
			rep.Counts["missing_in_db"]++
			rep.Items = append(rep.Items, toolCatalogEntry{
				Key: key, Status: "missing_in_db", CodeHash: ch,
				Note: "代码里有、库里没有（迁移未落库），模型看不到这个工具",
			})
		}
	}
	rep.CodeFingerprint = catalogFingerprint(commonCode)
	rep.RowFingerprint = catalogFingerprint(commonRow)
	rep.Drifted = rep.CodeFingerprint != rep.RowFingerprint
	return rep
}

// syncToolCatalog 在启动时把「运维没改过」的工具说明对齐到代码默认值（幂等）。
// 返回被改动的 key 数，便于调用方打日志。
func (s *Server) syncToolCatalog() int {
	pg := s.m.PG()
	if pg == nil {
		return 0
	}
	if v, _, _ := pg.GetSetting(toolCatalogSyncSetting); v == "off" {
		log.Print(logT("[tools] 工具目录同步已按设置关闭（tool_catalog_sync=off），仅提供 /api/tools/drift 诊断"))
		return 0
	}
	seeds := s.codeToolSeeds()
	rows, err := pg.ListTools()
	if err != nil {
		log.Printf("[tools] 工具目录同步：读取失败 %v", err)
		return 0
	}
	var refreshed, adopted, kept, inSync []string
	for _, row := range rows {
		if row == nil || !row.System {
			continue
		}
		sd, ok := seeds[row.Key]
		if !ok {
			continue
		}
		codeHash := toolSeedHash(sd.Desc, toolSeedJSON(sd.Schema))
		rowHash := toolSeedHash(row.Description, row.Schema)
		stored, _, _ := pg.GetSetting(toolSeedHashPrefix + row.Key)

		// 运维改过：永久保留，只记账不覆盖。
		if stored != "" && stored != rowHash {
			kept = append(kept, row.Key)
			continue
		}
		if rowHash == codeHash {
			if stored == "" {
				_ = pg.SetSetting(toolSeedHashPrefix+row.Key, codeHash)
			}
			inSync = append(inSync, row.Key)
			continue
		}
		// 到这里：行内容仍是上次同步时的样子（或首次接管），代码侧已变 → 刷新。
		backup := map[string]any{"description": row.Description, "schema": json.RawMessage(row.Schema)}
		if b, err := json.Marshal(backup); err == nil {
			_ = pg.SetSetting(toolSeedBackupPrefix+row.Key, string(b))
		}
		if err := pg.RefreshToolDefaults(row.Key, sd.Desc, toolSeedJSON(sd.Schema)); err != nil {
			log.Printf("[tools] 同步 %s 失败: %v", row.Key, err)
			continue
		}
		_ = pg.SetSetting(toolSeedHashPrefix+row.Key, codeHash)
		if stored == "" {
			adopted = append(adopted, row.Key)
		} else {
			refreshed = append(refreshed, row.Key)
		}
	}
	if len(adopted) > 0 {
		log.Printf(logT("[tools] 工具目录首次接管：把 %d 个老库工具说明对齐到代码默认值（旧值已备份到 settings toolseed_backup:*）：%v"), len(adopted), adopted)
	}
	if len(refreshed) > 0 {
		log.Printf(logT("[tools] 工具目录已随代码更新：%d 个 → %v"), len(refreshed), refreshed)
	}
	if len(kept) > 0 {
		log.Printf(logT("[tools] 工具目录保留本地自定义（未覆盖）：%v"), kept)
	}
	rep := s.toolCatalogDrift()
	log.Printf(logT("[tools] 工具目录指纹 code=%s row=%s 一致=%d 差异=%d 本地自定义=%d"),
		rep.CodeFingerprint, rep.RowFingerprint, rep.Counts["in_sync"], rep.Counts["drifted"], rep.Counts["kept_local"])
	return len(adopted) + len(refreshed)
}

// pgToolCatalogDrift 是只读诊断入口：GET /api/tools/drift。
// 三端各 curl 一次，比较 code_fingerprint 即可判断「模型看到的工具说明书是否同一份」。
func (s *Server) pgToolCatalogDrift(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	writeJSON(w, 200, s.toolCatalogDrift())
}
