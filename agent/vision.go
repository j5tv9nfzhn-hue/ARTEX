package agent

// ---------- 视觉桥(vision bridge) ----------
//
// 为什么存在这一层：norma SDK 的消息模型(llm.ContentBlock)只有 text / thinking /
// tool_use / tool_result 四种块，**没有 image 块**——上游 SDK 不承载图片。因此
// 「让模型看见图片」无法靠在既有消息里塞一个 image 块实现，只能在 artex 这一层
// 多做一次真正的多模态请求：把图片按各家 wire 规范发给同一个模型，取回文本转写，
// 再把转写注入用户消息。SDK 仍然只处理文本，而视觉能力真实可用。
//
// 三家 wire 的差异(2026-10 核对官方文档，任何一处写错都会变成「模型看不见图」)：
//
//	OpenAI Chat Completions  POST {base}/chat/completions
//	  content: [{"type":"text","text":...},
//	            {"type":"image_url","image_url":{"url":"data:image/png;base64,....","detail":"high"}}]
//	  → url 必须带 data:<mime>;base64, 前缀
//	OpenAI Responses        POST {base}/responses
//	  input[].content: [{"type":"input_text",...},
//	                    {"type":"input_image","image_url":"data:image/png;base64,...."}]
//	Anthropic Messages      POST {base}/v1/messages
//	  content: [{"type":"image",
//	             "source":{"type":"base64","media_type":"image/png","data":"<裸 base64>"}}, ...]
//	  → data 是【裸 base64】，带 data: 前缀会 400；media_type 必须与真实字节一致
//
// 尺寸：Anthropic 单图 base64 ≤10MB(Bedrock ≤5MB)，OpenAI 会把大图缩到 2048 内。
// 这里在本地先按 visionMaxDim 缩放、必要时重编码成 JPEG，避免被上游拒或计费暴涨。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
)

const (
	// visionCacheSuffix 是转写缓存文件后缀，紧挨着原图落盘：
	// <image>.vision.txt。首行是键(sha256/model/format)，其后是正文。
	visionCacheSuffix = ".vision.txt"
	// visionMaxBytes 是送给上游的单图字节上限。取三家里最严的一侧：
	// Anthropic base64 ≤10MB(≈7.5MB 原始)、Bedrock ≤5MB base64(≈3.75MB 原始)。
	visionMaxBytes = 4 << 20
	// visionMaxDim 是单边像素上限：超过就先本地缩放，别指望上游替你省。
	visionMaxDim = 2048
	// visionReplyMaxChars 兜住异常巨大的转写，避免把上下文一次性撑爆。
	visionReplyMaxChars = 12000
	// visionRequestTimeout 是单张图的超时；视觉请求比纯文本慢得多。
	visionRequestTimeout = 120 * time.Second
)

// visionTranscribePrompt 是给「同一个模型」的转写指令。要点：只做忠实转录，
// 不做推测/建议——推测会让下游 agent 把幻觉当成图片内容。
const visionTranscribePrompt = `你是图像转写器。用户上传了一张图片，但下游模型只能读文本，所以请你把这张图片完整、忠实地转写成文本。

要求：
1. 逐字转录图中出现的所有文字，保留原始语言、大小写与标点；表格用 Markdown 表格还原。
2. 描述界面结构：标题、导航、表单字段及其当前值、按钮、菜单、状态提示、错误信息、URL、版本号、验证码。
3. 若是终端/日志/代码截图，按原文转写命令与输出；若是图表或拓扑图，说明结构、节点与关键数值。
4. 若是架构/流程图，按连接关系描述；若是照片，描述可见物体与场景。
5. 不要推测图中没有的内容，不要给建议，不要省略「看起来不重要」的文字。

直接输出转写正文，不要前后缀说明。`

// VisionText 是一张图片的转写结果。
type VisionText struct {
	Path      string // 原图绝对路径
	Name      string // 原文件名(用于拼接提示)
	Text      string // 转写正文
	Cached    bool   // 是否命中磁盘缓存(未重复计费)
	CachePath string // 全文落盘位置（<原图>.vision.txt），注入被截断时指向它
}

// DescribeImage 对一张图片做一次真正的多模态请求，返回文本转写。
//
// 稳定优先：不做重试(网关抖动交给调用方呈现)、不改任何全局配置、结果按
// sha256+model+format 落盘缓存，同一张图同一模型只花一次钱。
func DescribeImage(ctx context.Context, cfg Config, absPath string) (VisionText, error) {
	out := VisionText{Path: absPath, Name: filepath.Base(absPath), CachePath: visionCachePath(absPath)}
	mime, data, err := loadVisionImage(absPath)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])

	if text, ok := readVisionCache(absPath, key, cfg); ok {
		out.Text, out.Cached = text, true
		return out, nil
	}

	ctx, cancel := context.WithTimeout(ctx, visionRequestTimeout)
	defer cancel()

	url, body, headers, err := buildVisionRequest(cfg, mime, data)
	if err != nil {
		return out, err
	}
	text, err := doVisionRequest(ctx, cfg, url, headers, body)
	if err != nil {
		return out, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return out, fmt.Errorf("视觉模型返回了空内容（模型可能不支持图片输入，或网关丢弃了 image 块）")
	}
	if r := []rune(text); len(r) > visionReplyMaxChars {
		text = string(r[:visionReplyMaxChars]) + "\n…（转写过长，已截断）"
	}
	out.Text = text
	writeVisionCache(absPath, key, cfg, text) // 缓存失败不影响本轮
	return out, nil
}

// ---------- 图片读取与缩放 ----------

// loadVisionImage 读取图片并按上游限制做必要的缩放/重编码，返回最终要发送的
// MIME 与字节。返回的字节就是计算缓存键的输入——发什么就缓存什么，不会串味。
func loadVisionImage(path string) (string, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("读取图片失败：%w", err)
	}
	defer f.Close()
	// 先按 32MiB 读够判断，超大文件直接拒绝，避免把内存打满。
	raw, err := io.ReadAll(io.LimitReader(f, 32<<20))
	if err != nil {
		return "", nil, fmt.Errorf("读取图片失败：%w", err)
	}
	if len(raw) == 0 {
		return "", nil, fmt.Errorf("图片为空文件")
	}
	mime, ok := sniffImageMIME(raw)
	if !ok {
		return "", nil, fmt.Errorf("不支持的图片格式（仅支持 PNG/JPEG/GIF/WebP）")
	}

	// 够小就不动它：不缩放、不重编码，原始字节直接发，转写质量最好。
	if len(raw) <= visionMaxBytes && !needsResize(raw, mime) {
		return mime, raw, nil
	}

	img, _, derr := image.Decode(bytes.NewReader(raw))
	if derr != nil {
		// WebP 等 stdlib 解不了的格式：能发就发，超限就明确报错，绝不静默截断
		//（截断的字节在上游必定解码失败，报错比 400 更难查）。
		if len(raw) <= visionMaxBytes {
			return mime, raw, nil
		}
		return "", nil, fmt.Errorf("图片超过 %d MiB 且格式无法本地缩放（WebP 请先转成 PNG/JPEG）", visionMaxBytes>>20)
	}
	scaled := resizeToFit(img, visionMaxDim)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flattenWhite(scaled), &jpeg.Options{Quality: 82}); err != nil {
		return "", nil, fmt.Errorf("图片重编码失败：%w", err)
	}
	if buf.Len() > visionMaxBytes {
		return "", nil, fmt.Errorf("图片缩放后仍超过 %d MiB，请先手工压缩", visionMaxBytes>>20)
	}
	return "image/jpeg", buf.Bytes(), nil
}

// sniffImageMIME 按魔数判类型，不信扩展名（截图存错后缀很常见）。
func sniffImageMIME(b []byte) (string, bool) {
	switch http.DetectContentType(b) {
	case "image/png":
		return "image/png", true
	case "image/jpeg":
		return "image/jpeg", true
	case "image/gif":
		return "image/gif", true
	case "image/webp":
		return "image/webp", true
	}
	return "", false
}

// needsResize 只解码尺寸头，不解整张图——大图判定要便宜。
func needsResize(raw []byte, mime string) bool {
	switch mime {
	case "image/png":
		if c, err := png.DecodeConfig(bytes.NewReader(raw)); err == nil {
			return c.Width > visionMaxDim || c.Height > visionMaxDim
		}
	case "image/jpeg":
		if c, err := jpeg.DecodeConfig(bytes.NewReader(raw)); err == nil {
			return c.Width > visionMaxDim || c.Height > visionMaxDim
		}
	case "image/gif":
		if c, err := gif.DecodeConfig(bytes.NewReader(raw)); err == nil {
			return c.Width > visionMaxDim || c.Height > visionMaxDim
		}
	}
	return false
}

// resizeToFit 用盒式平均把图缩到单边 ≤maxDim。只用 stdlib（x/image/draw 不在
// 依赖里，不为一次缩放引新依赖——那是拿稳定性换几行代码）。
func resizeToFit(src image.Image, maxDim int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return src
	}
	long := w
	if h > long {
		long = h
	}
	if long <= maxDim {
		return src
	}
	scale := float64(maxDim) / float64(long)
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy0 := b.Min.Y + y*h/nh
		sy1 := b.Min.Y + (y+1)*h/nh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < nw; x++ {
			sx0 := b.Min.X + x*w/nw
			sx1 := b.Min.X + (x+1)*w/nw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var sr, sg, sb, sa, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					r, g, bl, a := src.At(sx, sy).RGBA() // 预乘 alpha，16-bit
					sr += uint64(r)
					sg += uint64(g)
					sb += uint64(bl)
					sa += uint64(a)
					n++
				}
			}
			if n == 0 {
				continue
			}
			ar := uint8(sr / n >> 8)
			ag := uint8(sg / n >> 8)
			ab := uint8(sb / n >> 8)
			aa := uint8(sa / n >> 8)
			// 预乘 → 直通白底（JPEG 没有 alpha，透明区不处理会变黑块）。
			inv := 255 - aa
			dst.SetRGBA(x, y, color.RGBA{
				R: addSat(ar, inv), G: addSat(ag, inv), B: addSat(ab, inv), A: 255,
			})
		}
	}
	return dst
}

func addSat(a, b uint8) uint8 {
	if s := int(a) + int(b); s < 255 {
		return uint8(s)
	}
	return 255
}

// flattenWhite 把任意图拍平成不透明 RGB（JPEG 编码前的兜底）。
func flattenWhite(src image.Image) image.Image {
	if _, ok := src.(*image.RGBA); ok {
		return src
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x, y, src.At(x, y))
		}
	}
	return dst
}

// ---------- 请求构造 ----------

// buildVisionRequest 按配置的 wire 格式构造请求。三种格式的块形状互不兼容，
// 这里逐一对齐官方规范——这正是「模型声明支持图片却看不见」最常见的根因。
func buildVisionRequest(cfg Config, mime string, data []byte) (string, []byte, http.Header, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return "", nil, nil, fmt.Errorf("未配置模型名，无法调用视觉接口")
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	dataURL := "data:" + mime + ";base64," + b64
	h := http.Header{}
	h.Set("content-type", "application/json")

	switch cfg.Format {
	case llm.FormatOpenAI:
		h.Set("authorization", "Bearer "+cfg.APIKey)
		body := map[string]any{
			"model": cfg.Model,
			"messages": []any{map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": visionTranscribePrompt},
					map[string]any{"type": "image_url", "image_url": map[string]any{
						"url": dataURL, "detail": "high",
					}},
				},
			}},
			"max_tokens": 2048,
			"stream":     false,
		}
		raw, err := json.Marshal(body)
		return baseURLOr(cfg.BaseURL, "https://api.openai.com/v1") + "/chat/completions", raw, h, err

	case llm.FormatOpenAIResponses:
		h.Set("authorization", "Bearer "+cfg.APIKey)
		body := map[string]any{
			"model": cfg.Model,
			"input": []any{map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": visionTranscribePrompt},
					map[string]any{"type": "input_image", "image_url": dataURL},
				},
			}},
			"max_output_tokens": 2048,
		}
		raw, err := json.Marshal(body)
		return baseURLOr(cfg.BaseURL, "https://api.openai.com/v1") + "/responses", raw, h, err

	default: // llm.FormatAnthropic
		h.Set("x-api-key", cfg.APIKey)
		h.Set("anthropic-version", "2023-06-01")
		body := map[string]any{
			"model":      cfg.Model,
			"max_tokens": 2048,
			"messages": []any{map[string]any{
				"role": "user",
				"content": []any{
					// 图在前、文字在后：Anthropic 官方推荐顺序，也避免「先读到问题
					// 再看到图」导致的答非所问。
					map[string]any{"type": "image", "source": map[string]any{
						"type": "base64", "media_type": mime, "data": b64,
					}},
					map[string]any{"type": "text", "text": visionTranscribePrompt},
				},
			}},
		}
		raw, err := json.Marshal(body)
		return baseURLOr(cfg.BaseURL, "https://api.anthropic.com") + "/v1/messages", raw, h, err
	}
}

func baseURLOr(base, fallback string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return fallback
	}
	return base
}

// doVisionRequest 发请求并抽取文本。错误里带状态码与截断的响应体——排查
// 「模型不支持图片」时，上游那句话就是唯一线索。
func doVisionRequest(ctx context.Context, cfg Config, url string, headers http.Header, body []byte) (string, error) {
	client, err := quotaAwareHTTPClient(cfg.Proxy, cfg.SessionHeaderKey)
	if err != nil {
		return "", fmt.Errorf("创建 HTTP 客户端失败：%w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("视觉请求失败：%w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(raw))
		if r := []rune(snippet); len(r) > 400 {
			snippet = string(r[:400]) + "…"
		}
		return "", fmt.Errorf("视觉接口返回 HTTP %d：%s", resp.StatusCode, snippet)
	}
	return extractVisionText(cfg.Format, raw)
}

// extractVisionText 按格式取正文。三种响应结构的取值路径完全不同，统一在这里。
func extractVisionText(format llm.Format, raw []byte) (string, error) {
	switch format {
	case llm.FormatOpenAI:
		var r struct {
			Choices []struct {
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", fmt.Errorf("解析 OpenAI 响应失败：%w", err)
		}
		if len(r.Choices) == 0 {
			return "", fmt.Errorf("OpenAI 响应没有 choices")
		}
		return contentToString(r.Choices[0].Message.Content)

	case llm.FormatOpenAIResponses:
		var r struct {
			Output []struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", fmt.Errorf("解析 Responses 响应失败：%w", err)
		}
		var b strings.Builder
		for _, o := range r.Output {
			for _, c := range o.Content {
				if c.Type == "output_text" && c.Text != "" {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(c.Text)
				}
			}
		}
		return b.String(), nil

	default: // anthropic
		var r struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return "", fmt.Errorf("解析 Anthropic 响应失败：%w", err)
		}
		var b strings.Builder
		for _, c := range r.Content {
			if c.Type == "text" && c.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(c.Text)
			}
		}
		return b.String(), nil
	}
}

// contentToString 兼容 content 为字符串（常规）或内容块数组（部分网关）。
func contentToString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("响应缺少 content")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("无法解析 content：%w", err)
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return b.String(), nil
}

// ---------- 磁盘缓存 ----------

func visionCachePath(imgPath string) string { return imgPath + visionCacheSuffix }

// readVisionCache 命中条件：sha256(实际发送的字节) + 模型 + 格式三者一致。
// 换模型就必须重转——不同模型的转写质量与侧重不同，不能混用。
func readVisionCache(imgPath, key string, cfg Config) (string, bool) {
	raw, err := os.ReadFile(visionCachePath(imgPath))
	if err != nil {
		return "", false
	}
	head, body, ok := strings.Cut(string(raw), "\n")
	if !ok {
		return "", false
	}
	want := fmt.Sprintf("sha256=%s model=%s format=%s", key, cfg.Model, cfg.Format)
	if strings.TrimSpace(head) != want {
		return "", false
	}
	text := strings.TrimSpace(body)
	if text == "" {
		return "", false
	}
	return text, true
}

func writeVisionCache(imgPath, key string, cfg Config, text string) {
	head := fmt.Sprintf("sha256=%s model=%s format=%s\n", key, cfg.Model, cfg.Format)
	_ = os.WriteFile(visionCachePath(imgPath), []byte(head+text+"\n"), 0o644)
}
