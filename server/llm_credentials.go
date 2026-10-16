package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// 凭据实体 API（计划 46–47）：独立的 credentials 资源，可被多个 LLM 配置引用。
//
//   - GET    /api/llm/credentials        列出凭据（只含 api_key_hint，绝无 key）
//   - POST   /api/llm/credentials        新建（id 缺省/0）或更新（id>0；空 api_key 保持原值）
//   - DELETE /api/llm/credentials/{id}   删除并解绑引用它的配置
//
// 响应 id 用字符串（与其它读接口一致）；请求体 id 用数字（与其它写接口一致）。
// key 的静态加密在 db 层（凭据行与 llm_profiles.api_key 同一套 enc:v1: 口径），
// 这里只负责「明文进、密文出」的方向控制与掩码语义。

const (
	errCredNameRequired = "凭据名称不能为空"
	errCredKeyRequired  = "新建凭据必须填写 API Key"
	errCredMaskedNoBase = "提交的是掩码值，但库中没有可保持的原值：请填写完整 API Key"
	errCredNameConflict = "凭据名称已存在"
)

// CredentialDTO is one credential as shown in the UI: no key, only the tail hint.
type CredentialDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	APIKeyHint string `json:"api_key_hint,omitempty"`
	// ProfileNames lists the LLM profiles currently bound to this credential
	// (delete confirmation shows "将解绑 N 个配置").
	ProfileNames []string  `json:"profile_names,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func credentialDTO(c *db.Credential) CredentialDTO {
	return CredentialDTO{
		ID:           i64s(c.ID),
		Name:         c.Name,
		APIKeyHint:   c.APIKeyHint,
		ProfileNames: c.ProfileNames,
		CreatedAt:    c.CreatedAt,
		UpdatedAt:    c.UpdatedAt,
	}
}

func (s *Server) pgListCredentials(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	noStore(w) // 凭据 hint 与引用关系
	cs, err := pg.ListCredentials()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]CredentialDTO, 0, len(cs))
	for _, c := range cs {
		out = append(out, credentialDTO(c))
	}
	writeJSON(w, 200, map[string]any{"credentials": out})
}

func (s *Server) pgSaveCredential(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		ID     int64  `json:"id"` // 0/缺省 = 新建
		Name   string `json:"name"`
		APIKey string `json:"api_key"` // 更新时留空 = 保持原值
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.APIKey = strings.TrimSpace(body.APIKey)
	if body.Name == "" {
		writeErr(w, 400, errCredNameRequired)
		return
	}
	if notify.IsMasked(body.APIKey) {
		// 掩码值 = 「保持原值」的哨兵（同 notify.MergeConfig 的语义）。更新时等价
		// 于留空；新建没有原值可保持 → 拒绝，绝不把字面量存进库。
		if body.ID == 0 {
			writeErr(w, 400, errCredMaskedNoBase)
			return
		}
		body.APIKey = ""
	}
	if body.ID == 0 && body.APIKey == "" {
		writeErr(w, 400, errCredKeyRequired)
		return
	}
	id, err := pg.SaveCredential(&db.Credential{ID: body.ID, Name: body.Name, APIKey: body.APIKey})
	if err != nil {
		switch {
		case errors.Is(err, db.ErrCredentialNotFound):
			writeErr(w, 404, err.Error())
		case db.UniqueViolation(err):
			writeErr(w, 409, errCredNameConflict)
		default:
			writeErr(w, 500, err.Error())
		}
		return
	}
	// 绑定了该凭据的配置运行时以凭据行为准：缓存里的 agent/provider 必须重建，
	// 否则改完 key 仍然拿旧凭据发请求。
	s.invalidateProfileAgents()
	s.reapplyActiveProfile()
	writeJSON(w, 200, map[string]any{"id": i64s(id)})
}

func (s *Server) pgDeleteCredential(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, _ := pathInt(r, "id")
	unbound, err := pg.DeleteCredential(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 解绑后有效 Key 变了（可能变空），运行中的 provider/agent 缓存全部作废。
	s.invalidateProfileAgents()
	s.reapplyActiveProfile()
	writeJSON(w, 200, map[string]any{"deleted": i64s(id), "unbound_profiles": unbound})
}
