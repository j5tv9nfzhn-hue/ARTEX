package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/notify"
)

// 一次「测试连接 / 拉取模型」实际用的是谁的 Key。
//
// 为什么要把这件事显式说出来：Key 有三种来源——表单里刚粘进去的、配置里存的、
// 凭据实体里存的——在界面上长得一模一样。测试失败时用户只看到供应商那一句
// 401/403（"invalid api key" / "Missing Authentication header"），而「Key 填错了」
// 与「根本没读到 Key」在界面上无法区分，偏偏两者的修法完全相反。
//
// 之前的实现会在读配置失败时**静默回落到全局 Key**：配置解不出来 → 用全局那把
// 去打了 A 供应商 → 供应商当然说 Key 不对。错误信息离真正的原因隔了两次跳转。
type llmKeySource struct {
	Key    string // 明文，只在本次请求内使用；不写日志、不回前端
	Source string // 展示用来源：form / global / profile #12 xxx / credential #7 xxx
	Hint   string // 尾号，回给前端自证「用的确实是这把 Key」
}

var (
	errLLMMaskedKey = errors.New("这是掩码/省略值（只有尾号），不是完整 Key：请重新粘贴完整 Key")
	errLLMSpaceKey  = errors.New("Key 中间有空格或全角空格：请检查复制时是否带进了空白字符")
	errLLMCtrlKey   = errors.New("Key 里含换行或控制字符：请重新复制，注意不要带上换行")
)

// llmKeyHint 返回 Key 的末 4 位提示（"…a1b2"）。短 Key 不回尾号，避免把整把
// Key 当成提示原样回显。
func llmKeyHint(key string) string {
	r := []rune(strings.TrimSpace(key))
	if len(r) <= 8 {
		return ""
	}
	return "…" + string(r[len(r)-4:])
}

// normalizeLLMKey 把粘贴进来的 Key 归一化成可发送的形态，并在明显不是完整
// Key 时**当场**报错。
//
// 这些检查放在发请求之前，是因为供应商对它们的反应都一样——401——而用户看到
// 401 只会去怀疑 Key 本身。带引号、带换行、带首尾空白、只粘了界面上的省略值
// （"…UJe8"），这几种都是复制粘贴时的高频形态：
//   - 带引号：从 JSON/配置片段里复制；
//   - 带换行：从多行文档里复制，Go 的 http 客户端会因为头里有非法字节直接
//     拒绝发送，错误反而变成「请求没发出去」；
//   - 省略值：界面只显示尾号，用户以为那就是 Key。
func normalizeLLMKey(raw string) (string, error) {
	k := strings.TrimSpace(raw)
	if k == "" {
		return "", nil
	}
	// 成对的引号是复制残留，不是 Key 的一部分。
	k = strings.TrimSpace(strings.Trim(k, `"'`))
	if k == "" {
		return "", nil
	}
	if notify.IsMasked(k) || strings.HasPrefix(k, "…") || strings.HasPrefix(k, "...") {
		return "", errLLMMaskedKey
	}
	for _, r := range k {
		switch {
		case r == ' ' || r == '\u00a0' || r == '\u3000':
			return "", errLLMSpaceKey
		case r < 0x20 || r == 0x7f:
			return "", errLLMCtrlKey
		}
	}
	return k, nil
}

// resolveLLMKey 决定这次请求用哪把 Key，并把「用的是谁的」一起带出去。
//
// 优先级与用户预期一致：表单输入 > 指定配置（含其绑定的凭据）> 全局配置。
// 与旧实现的关键差别是**失败不再回落到下一级**：指定了 profile_id 却读不出来，
// 就带着原因返回，绝不用另一把 Key 去替它发请求。
func (s *Server) resolveLLMKey(formKey string, profileID *int64) (llmKeySource, error) {
	if k, err := normalizeLLMKey(formKey); err != nil {
		return llmKeySource{}, err
	} else if k != "" {
		return llmKeySource{Key: k, Source: "form", Hint: llmKeyHint(k)}, nil
	}

	if profileID != nil {
		if s.m == nil || s.m.pg == nil {
			return llmKeySource{}, errors.New("数据库未就绪，无法读取已保存配置的 Key")
		}
		p, err := s.m.pg.ProfileByID(*profileID)
		if err != nil {
			// 典型来源：主密钥变了/凭据密文解不开。绝不能回落——那会把
			// 「读不出来」伪装成「供应商说 Key 错了」。
			return llmKeySource{}, fmt.Errorf("读取配置 #%d 的 Key 失败：%w", *profileID, err)
		}
		if p == nil {
			return llmKeySource{}, fmt.Errorf("配置 #%d 不存在（可能已被删除），请重新选择配置", *profileID)
		}
		src := fmt.Sprintf("配置 #%d「%s」", p.ID, p.Name)
		k, err := normalizeLLMKey(p.APIKey)
		if err != nil {
			return llmKeySource{}, fmt.Errorf("%s 里保存的 Key 不可用：%w", src, err)
		}
		if k == "" {
			return llmKeySource{}, fmt.Errorf("%s 没有可用的 API Key：请到它所属的供应商里补全后再测", src)
		}
		return llmKeySource{Key: k, Source: src, Hint: llmKeyHint(k)}, nil
	}

	s.cfgMu.Lock()
	global := strings.TrimSpace(s.llmCfg.APIKey)
	s.cfgMu.Unlock()
	if global != "" {
		if k, err := normalizeLLMKey(global); err == nil && k != "" {
			return llmKeySource{Key: k, Source: "全局配置", Hint: llmKeyHint(k)}, nil
		}
	}
	// 文案与既有前端/测试一致：这条路径就是「哪儿都没填 Key」。
	return llmKeySource{}, errors.New(errLLMTestNoAPIKey)
}
