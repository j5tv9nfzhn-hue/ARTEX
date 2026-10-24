package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// ---------- LLM 供应商 ----------
//
// 一家供应商 = 端点（format / base_url / proxy）+ 鉴权（凭据或内联 Key）+
// 端点级限速与会话头。模型挂在它下面，只保留「模型自己」的参数。
//
// 这一层刻意不重写旧接口：GET/POST /api/llm/profiles 继续存在、继续返回扁平
// 配置。新界面用 providers 接口拿分组树；旧界面与手机端用 profiles 接口，
// 两者读写的是同一批行。

// pgListProviders 返回供应商树（含各自名下的模型）。设置页一次拉完，避免
// 「N 个供应商 → N 个请求」。
func (s *Server) pgListProviders(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	noStore(w) // api_key_hint / 凭据绑定状态
	vs, err := pg.ListProviders()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"providers": llmProviderDTOs(vs)})
}

// pgSaveProvider 新建或更新一家供应商。
//
// credential_id 语义与模型保存一致：省略 = 不动绑定，0 = 解绑回内联 Key，
// >0 = 绑定该凭据。
func (s *Server) pgSaveProvider(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		db.LLMProvider
		APIKey string `json:"api_key"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	v := body.LLMProvider
	v.APIKey = strings.TrimSpace(body.APIKey)
	// 掩码值是「保持原值」的哨兵（同模型保存与通知渠道）。把 "__masked__:…xxxx"
	// 当成新 Key 加密存库会让鉴权永久失败，且错误离原因极远。
	if notify.IsMasked(v.APIKey) {
		v.APIKey = ""
	}
	id, err := pg.SaveProvider(&v)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrProviderNameUsed):
			writeErr(w, 409, err.Error())
		default:
			writeErr(w, 500, err.Error())
		}
		return
	}
	// 端点或 Key 变了：已构建的 provider（含共享限速器）必须重建，运行中的引擎
	// 不能继续拿旧端点发请求。
	s.invalidateProfileAgents()
	s.reapplyActiveProfile()
	writeJSON(w, 200, map[string]any{"id": i64s(id)})
}

// pgDeleteProvider 删除一家供应商。名下还有模型时拒绝——删模型会级联到任务链、
// Agent 绑定与会话，设置页不该有这种隐形副作用。
func (s *Server) pgDeleteProvider(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, _ := pathInt(r, "id")
	// 级联：该供应商名下的模型一并删除，不再要求用户先逐个清空。
	models, err := pg.DeleteProvider(id)
	switch {
	case err == nil:
	case errors.Is(err, db.ErrProviderNotFound):
		writeErr(w, 404, err.Error())
		return
	default:
		writeErr(w, 500, err.Error())
		return
	}
	// 被删掉的模型可能是激活的那条，也可能是某些任务链上的候选：前者要立刻把
	// 引擎切到新的激活配置（数据库里激活位已经交接好了），后者靠 DeleteProvider
	// 里 bump 过的链版本号在下一轮重新解析。没有激活配置时 reapply 是空操作。
	s.invalidateProfileAgents()
	s.reapplyActiveProfile()
	writeJSON(w, 200, map[string]any{"deleted": i64s(id), "models_deleted": models})
}

// pgMoveProfileProvider 把一条模型挪到另一家供应商下（建错位置时不用重建配置）。
// 端点随之改变，窗口/输出上限照旧由用户手填的那两个值决定。
func (s *Server) pgMoveProfileProvider(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的模型 id")
		return
	}
	var body struct {
		ProviderID int64 `json:"provider_id"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.MoveProfileToProvider(id, body.ProviderID); err != nil {
		switch {
		case errors.Is(err, db.ErrProviderNotFound), errors.Is(err, db.ErrLLMProfileNotFound):
			writeErr(w, 404, err.Error())
		default:
			writeErr(w, 400, err.Error())
		}
		return
	}
	s.invalidateProfileAgents()
	s.reapplyActiveProfile()
	writeJSON(w, 200, map[string]any{"ok": true})
}
