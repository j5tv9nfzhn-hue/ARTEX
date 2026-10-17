package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

func (t *ToolSet) netProbeTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "net_probe",
		Description: "原生网络端口扫描、TLS 证书检视、原始未归一化 HTTP(raw_http)、WebSocket(ws/wss) 交互与本地带外回调监听(oast_callback)工具（彻底替代 Python socket/ssl/websocket-client/http.server 脚本）。支持 4 种模式：port_scan（默认，并发 TCP/TLS 端口扫描、TLS SANs 证书提取与原始协议 Banner 抓取）、raw_http（绕过 Go net/http 路径清洗，原样发送含 /..;/、/../、CRLF 或畸形头的原始 HTTP 请求，测路径穿越与走私）、websocket（发起 ws:// 或 wss:// 握手、自定义 Origin 测试 CSWSH 跨站劫持并收发帧消息）、oast_callback（在进程内启动临时带外回调监听器，自动替换 {{CALLBACK_URL}} 触发 SSRF/XXE/RCE 并捕获回连报文）。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mode": map[string]any{
					"type":        "string",
					"description": "探测模式：port_scan（默认）| raw_http（未归一化原始 HTTP 报文）| websocket（WS/WSS 握手与帧收发）| oast_callback（本地 OOB 带外回调监听与触发）",
				},
				"host":          map[string]any{"type": "string", "description": "目标主机名或 IP（port_scan / raw_http 模式使用）"},
				"ports":         map[string]any{"type": "string", "description": "port_scan 端口规格，支持 '80,443,8080' 或范围 '8000-8020'（单次最多 256 个端口，默认常用 Web/服务端口）"},
				"tls":           map[string]any{"type": "boolean", "description": "是否使用 TLS 连接"},
				"sni":           map[string]any{"type": "string", "description": "TLS SNI 域名（默认同 host）"},
				"send_text":     map[string]any{"type": "string", "description": "port_scan 连接建立后发送的明文探测载荷（如 'INFO\\r\\n'）"},
				"send_hex":      map[string]any{"type": "string", "description": "port_scan 连接建立后发送的十六进制字节载荷"},
				"raw_request":   map[string]any{"type": "string", "description": "mode=raw_http 时的完整原始 HTTP 请求报文（换行自动规范为 \\r\\n）；若仅需发送未归一化路径也可传 url + raw_path"},
				"raw_path":      map[string]any{"type": "string", "description": "mode=raw_http 时不经任何 URL 清洗直接发送的原始请求路径（如 '/api/..;/admin/list' 或 '/static/../../../etc/passwd'）"},
				"url":           map[string]any{"type": "string", "description": "mode=websocket 的 ws:// 或 wss:// 地址，或 mode=raw_http 的目标基础 URL，或 mode=oast_callback 的触发请求 URL（支持 {{CALLBACK_URL}} 和 {{CALLBACK_HOST_PORT}} 占位符）"},
				"method":        map[string]any{"type": "string", "description": "mode=raw_http 或 mode=oast_callback 触发请求的 HTTP 方法，默认 GET"},
				"headers":       map[string]any{"type": "object", "description": "mode=raw_http / websocket / oast_callback 的自定义请求头（如 websocket 传 {'Origin': 'https://evil.com'} 测试 CSWSH）"},
				"body":          map[string]any{"type": "string", "description": "mode=raw_http 或 mode=oast_callback 触发请求的请求体（支持 {{CALLBACK_URL}} 占位符）"},
				"ws_messages":   map[string]any{"type": "array", "description": "mode=websocket 握手成功后依次发送的文本消息列表", "items": map[string]any{"type": "string"}},
				"listen_port":   map[string]any{"type": "integer", "description": "mode=oast_callback 的本地监听端口（默认 0 自动分配空闲端口）"},
				"callback_host": map[string]any{"type": "string", "description": "mode=oast_callback 用于拼接 {{CALLBACK_URL}} 的本机可达 IP/Host（默认自动探测本机出网 IP 或 127.0.0.1）"},
				"concurrency":   map[string]any{"type": "integer", "description": "port_scan 并发探测数，默认 10，最大 16"},
				"timeout_ms":    map[string]any{"type": "integer", "description": "超时时间（毫秒），默认 4000"},
			},
		},
		ReadOnly:    noStoreRo,
		Permissions: allowAllPerm,
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Mode         string            `json:"mode"`
				Host         string            `json:"host"`
				Ports        string            `json:"ports"`
				TLS          bool              `json:"tls"`
				SNI          string            `json:"sni"`
				SendText     string            `json:"send_text"`
				SendHex      string            `json:"send_hex"`
				RawRequest   string            `json:"raw_request"`
				RawPath      string            `json:"raw_path"`
				URL          string            `json:"url"`
				Method       string            `json:"method"`
				Headers      map[string]string `json:"headers"`
				Body         string            `json:"body"`
				WSMessages   []string          `json:"ws_messages"`
				ListenPort   int               `json:"listen_port"`
				CallbackHost string            `json:"callback_host"`
				Concurrency  int               `json:"concurrency"`
				TimeoutMs    int               `json:"timeout_ms"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			mode := strings.ToLower(strings.TrimSpace(a.Mode))
			if mode == "" {
				mode = "port_scan"
			}
			timeoutDur := time.Duration(defaultInt(a.TimeoutMs, 4000, 300, 30000)) * time.Millisecond

			switch mode {
			case "raw_http":
				res, err := t.runRawHTTPProbe(ctx, a.Host, a.URL, a.Method, a.RawPath, a.RawRequest, a.Headers, a.Body, a.TLS, a.SNI, timeoutDur)
				if err != nil {
					return actool.Errorf("raw_http 执行失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "websocket":
				wsURL := strings.TrimSpace(a.URL)
				if wsURL == "" {
					return actool.Errorf("mode=websocket 时 url 为必填参数（ws:// 或 wss://）"), nil
				}
				res, err := runWebSocketProbe(ctx, wsURL, a.Headers, a.WSMessages, timeoutDur)
				if err != nil {
					return actool.Errorf("websocket 探测失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil

			case "oast_callback":
				res, err := t.runOASTCallbackProbe(ctx, a.ListenPort, a.CallbackHost, a.Method, a.URL, a.Headers, a.Body, timeoutDur)
				if err != nil {
					return actool.Errorf("oast_callback 执行失败: " + err.Error()), nil
				}
				b, _ := json.MarshalIndent(res, "", "  ")
				return actool.Text(actool.Capture(tc, string(b))), nil
			}

			host := strings.TrimSpace(a.Host)
			if host == "" {
				return actool.Errorf("host 为必填参数"), nil
			}
			host = strings.TrimPrefix(strings.TrimPrefix(host, "http://"), "https://")
			if idx := strings.IndexByte(host, '/'); idx >= 0 {
				host = host[:idx]
			}
			if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
				host = h
			}

			portsSpec := strings.TrimSpace(a.Ports)
			if portsSpec == "" {
				portsSpec = "21,22,80,443,1433,3306,5432,6379,8000,8080,8443,8888,9000,9200,27017"
			}
			ports, err := parsePortSpec(portsSpec, 256)
			if err != nil {
				return actool.Errorf("ports 解析失败: " + err.Error()), nil
			}

			var payload []byte
			if a.SendHex != "" {
				decoded, err := hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(a.SendHex), " ", ""))
				if err != nil {
					return actool.Errorf("send_hex 解析失败: " + err.Error()), nil
				}
				payload = decoded
			} else if a.SendText != "" {
				payload = []byte(a.SendText)
			}

			concurrency := defaultInt(a.Concurrency, 10, 1, 16)
			sni := firstNonEmpty(strings.TrimSpace(a.SNI), host)

			type tlsCertSummary struct {
				Subject   string   `json:"subject"`
				Issuer    string   `json:"issuer"`
				DNSNames  []string `json:"dns_names,omitempty"`
				NotBefore string   `json:"not_before"`
				NotAfter  string   `json:"not_after"`
			}
			type portResult struct {
				Port      int             `json:"port"`
				Open      bool            `json:"open"`
				TLS       bool            `json:"tls,omitempty"`
				ElapsedMs int64           `json:"elapsed_ms"`
				Banner    string          `json:"banner,omitempty"`
				BannerHex string          `json:"banner_hex,omitempty"`
				Cert      *tlsCertSummary `json:"cert,omitempty"`
			}

			var (
				mu          sync.Mutex
				openResults []portResult
				closedCount int
				wg          sync.WaitGroup
				localSem    = make(chan struct{}, concurrency)
			)

			for _, p := range ports {
				if ctx.Err() != nil {
					break
				}
				wg.Add(1)
				localSem <- struct{}{}
				globalNetProbeSem <- struct{}{}
				go func(port int) {
					defer wg.Done()
					defer func() {
						<-globalNetProbeSem
						<-localSem
					}()
					if ctx.Err() != nil {
						return
					}
					addr := net.JoinHostPort(host, strconv.Itoa(port))
					t0 := time.Now()
					dialer := &net.Dialer{Timeout: timeoutDur}
					conn, err := dialer.DialContext(ctx, "tcp", addr)
					if err != nil {
						mu.Lock()
						closedCount++
						mu.Unlock()
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(timeoutDur))

					useTLS := a.TLS || port == 443 || port == 8443 || port == 9443
					var certInfo *tlsCertSummary
					if useTLS {
						tlsConn := tls.Client(conn, &tls.Config{
							InsecureSkipVerify: true,
							ServerName:         sni,
						})
						if err := tlsConn.HandshakeContext(ctx); err == nil {
							conn = tlsConn
							state := tlsConn.ConnectionState()
							if len(state.PeerCertificates) > 0 {
								c := state.PeerCertificates[0]
								certInfo = &tlsCertSummary{
									Subject:   c.Subject.String(),
									Issuer:    c.Issuer.String(),
									DNSNames:  c.DNSNames,
									NotBefore: c.NotBefore.Format(time.RFC3339),
									NotAfter:  c.NotAfter.Format(time.RFC3339),
								}
							}
						} else if a.TLS {
							mu.Lock()
							closedCount++
							mu.Unlock()
							return
						} else {
							useTLS = false
						}
					}

					if len(payload) > 0 {
						_, _ = conn.Write(payload)
					}

					buf := make([]byte, 2048)
					_ = conn.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
					n, _ := conn.Read(buf)
					elapsed := time.Since(t0).Milliseconds()

					pr := portResult{
						Port:      port,
						Open:      true,
						TLS:       useTLS,
						ElapsedMs: elapsed,
						Cert:      certInfo,
					}
					if n > 0 {
						raw := buf[:n]
						pr.Banner = clipUTF8(strings.TrimSpace(string(raw)), 512)
						if len(raw) <= 128 {
							pr.BannerHex = hex.EncodeToString(raw)
						}
					}

					mu.Lock()
					openResults = append(openResults, pr)
					mu.Unlock()
				}(p)
			}
			wg.Wait()

			sort.Slice(openResults, func(i, j int) bool {
				return openResults[i].Port < openResults[j].Port
			})

			out := map[string]any{
				"host":         host,
				"scanned":      len(ports),
				"open_count":   len(openResults),
				"closed_count": closedCount,
				"open_ports":   openResults,
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			return actool.Text(actool.Capture(tc, string(b))), nil
		},
	})
}

func (t *ToolSet) runRawHTTPProbe(
	ctx context.Context,
	host, rawURL, method, rawPath, rawReq string,
	headers map[string]string,
	body string,
	useTLS bool,
	sni string,
	timeoutDur time.Duration,
) (map[string]any, error) {
	targetHost := strings.TrimSpace(host)
	targetPort := ""
	if rawURL != "" {
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			rawURL = "http://" + rawURL
		}
		if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
			if strings.EqualFold(u.Scheme, "https") {
				useTLS = true
			}
			targetHost = u.Hostname()
			targetPort = u.Port()
			if rawPath == "" {
				rawPath = u.RequestURI()
			}
		}
	}
	if targetHost == "" {
		return nil, fmt.Errorf("mode=raw_http 需要提供 url 或 host")
	}
	if h, p, err := net.SplitHostPort(targetHost); err == nil {
		targetHost = h
		targetPort = p
	}
	if targetPort == "" {
		if useTLS {
			targetPort = "443"
		} else {
			targetPort = "80"
		}
	}
	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if rawPath == "" {
		rawPath = "/"
	}

	var wireReq string
	if strings.TrimSpace(rawReq) != "" {
		norm := strings.ReplaceAll(rawReq, "\r\n", "\n")
		wireReq = strings.ReplaceAll(norm, "\n", "\r\n")
		if !strings.Contains(wireReq, "\r\n\r\n") {
			wireReq += "\r\n\r\n"
		}
	} else {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", method, rawPath))
		hasHost := false
		hasConn := false
		for k, v := range headers {
			if strings.EqualFold(k, "Host") {
				hasHost = true
			}
			if strings.EqualFold(k, "Connection") {
				hasConn = true
			}
			sb.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
		}
		if !hasHost {
			sb.WriteString(fmt.Sprintf("Host: %s\r\n", targetHost))
		}
		if !hasConn {
			sb.WriteString("Connection: close\r\n")
		}
		if body != "" {
			sb.WriteString(fmt.Sprintf("Content-Length: %d\r\n", len(body)))
		}
		sb.WriteString("\r\n")
		sb.WriteString(body)
		wireReq = sb.String()
	}

	addr := net.JoinHostPort(targetHost, targetPort)
	t0 := time.Now()
	dialer := &net.Dialer{Timeout: timeoutDur}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeoutDur))

	if useTLS {
		tlsConn := tls.Client(conn, &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         firstNonEmpty(sni, targetHost),
		})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = tlsConn
	}

	if _, err := conn.Write([]byte(wireReq)); err != nil {
		return nil, err
	}
	rawResp, _ := io.ReadAll(io.LimitReader(conn, 512*1024))
	elapsed := time.Since(t0).Milliseconds()

	respText := string(rawResp)
	statusCode := 0
	respHeaders := make(http.Header)
	respBody := respText
	if resp, err := http.ReadResponse(bufio.NewReader(strings.NewReader(respText)), nil); err == nil {
		statusCode = resp.StatusCode
		respHeaders = resp.Header
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		respBody = string(b)
	}

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	fullURL := fmt.Sprintf("%s://%s:%s%s", scheme, targetHost, targetPort, rawPath)
	var exID string
	if t != nil && t.trafficStore != nil && statusCode > 0 {
		reqH := make(http.Header)
		for k, v := range headers {
			reqH.Set(k, v)
		}
		exID = t.trafficStore.RecordDirect(method, fullURL, "HTTP/1.1", reqH, []byte(body), statusCode, respHeaders, []byte(respBody))
	}

	return map[string]any{
		"mode":         "raw_http",
		"exchange_id":  exID,
		"target":       addr,
		"raw_path":     rawPath,
		"status":       statusCode,
		"elapsed_ms":   elapsed,
		"raw_length":   len(rawResp),
		"body_preview": clipUTF8(respBody, 2048),
	}, nil
}

func runWebSocketProbe(ctx context.Context, wsURL string, headers map[string]string, messages []string, timeoutDur time.Duration) (map[string]any, error) {
	u, err := url.Parse(wsURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("无效的 WebSocket URL: %s", wsURL)
	}
	useTLS := strings.EqualFold(u.Scheme, "wss") || strings.EqualFold(u.Scheme, "https")
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if useTLS {
			port = "443"
		} else {
			port = "80"
		}
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}

	dialer := &net.Dialer{Timeout: timeoutDur}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeoutDur))

	if useTLS {
		tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: host})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = tlsConn
	}

	keyBytes := make([]byte, 16)
	_, _ = rand.Read(keyBytes)
	wsKey := base64.StdEncoding.EncodeToString(keyBytes)

	var reqBuf strings.Builder
	reqBuf.WriteString(fmt.Sprintf("GET %s HTTP/1.1\r\n", path))
	reqBuf.WriteString(fmt.Sprintf("Host: %s\r\n", u.Host))
	reqBuf.WriteString("Upgrade: websocket\r\n")
	reqBuf.WriteString("Connection: Upgrade\r\n")
	reqBuf.WriteString(fmt.Sprintf("Sec-WebSocket-Key: %s\r\n", wsKey))
	reqBuf.WriteString("Sec-WebSocket-Version: 13\r\n")
	for k, v := range headers {
		reqBuf.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	reqBuf.WriteString("\r\n")

	if _, err := conn.Write([]byte(reqBuf.String())); err != nil {
		return nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 验证 Sec-WebSocket-Accept
	h := sha1.New()
	h.Write([]byte(wsKey + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	expectedAccept := base64.StdEncoding.EncodeToString(h.Sum(nil))
	upgraded := resp.StatusCode == 101 && resp.Header.Get("Sec-WebSocket-Accept") == expectedAccept

	var received []string
	if upgraded {
		for _, msg := range messages {
			frame := buildMaskedWSFrame([]byte(msg))
			_, _ = conn.Write(frame)
		}
		_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
		for i := 0; i < 10; i++ {
			msg, err := readWSFramePayload(br)
			if err != nil {
				break
			}
			received = append(received, clipUTF8(msg, 1024))
		}
	}

	return map[string]any{
		"mode":              "websocket",
		"url":               wsURL,
		"handshake_status":  resp.StatusCode,
		"upgraded":          upgraded,
		"sent_messages":     len(messages),
		"received_messages": received,
	}, nil
}

func buildMaskedWSFrame(payload []byte) []byte {
	var frame []byte
	frame = append(frame, 0x81) // FIN=1, Opcode=1 (Text)
	n := len(payload)
	switch {
	case n <= 125:
		frame = append(frame, byte(0x80|n))
	case n <= 65535:
		frame = append(frame, 0x80|126, byte(n>>8), byte(n))
	default:
		frame = append(frame, 0x80|127)
		b8 := make([]byte, 8)
		binary.BigEndian.PutUint64(b8, uint64(n))
		frame = append(frame, b8...)
	}
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	frame = append(frame, mask...)
	for i := 0; i < n; i++ {
		frame = append(frame, payload[i]^mask[i%4])
	}
	return frame
}

func readWSFramePayload(r *bufio.Reader) (string, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return "", err
	}
	masked := (hdr[1] & 0x80) != 0
	length := int64(hdr[1] & 0x7F)
	if length == 126 {
		b2 := make([]byte, 2)
		if _, err := io.ReadFull(r, b2); err != nil {
			return "", err
		}
		length = int64(binary.BigEndian.Uint16(b2))
	} else if length == 127 {
		b8 := make([]byte, 8)
		if _, err := io.ReadFull(r, b8); err != nil {
			return "", err
		}
		length = int64(binary.BigEndian.Uint64(b8))
	}
	if length > 64*1024 {
		length = 64 * 1024
	}
	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(r, mask); err != nil {
			return "", err
		}
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return "", err
	}
	if masked {
		for i := range data {
			data[i] ^= mask[i%4]
		}
	}
	return string(data), nil
}

func detectLocalOutboundIP() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:80", 800*time.Millisecond)
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil {
			return addr.IP.String()
		}
	}
	return "127.0.0.1"
}

func (t *ToolSet) runOASTCallbackProbe(
	ctx context.Context,
	listenPort int,
	callbackHost, method, triggerURL string,
	headers map[string]string,
	body string,
	timeoutDur time.Duration,
) (map[string]any, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", listenPort))
	if err != nil {
		return nil, err
	}
	defer ln.Close()

	actualPort := ln.Addr().(*net.TCPAddr).Port
	cbHost := strings.TrimSpace(callbackHost)
	if cbHost == "" {
		cbHost = detectLocalOutboundIP()
	}
	cbHostPort := fmt.Sprintf("%s:%d", cbHost, actualPort)
	cbURL := "http://" + cbHostPort + "/oast"

	type oastHit struct {
		RemoteAddr string `json:"remote_addr"`
		Timestamp  string `json:"timestamp"`
		RawData    string `json:"raw_data"`
	}
	var (
		mu   sync.Mutex
		hits []oastHit
	)

	waitCtx, cancel := context.WithTimeout(ctx, timeoutDur)
	defer cancel()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				buf := make([]byte, 4096)
				n, _ := c.Read(buf)
				_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nOK"))
				mu.Lock()
				if len(hits) < 20 {
					hits = append(hits, oastHit{
						RemoteAddr: c.RemoteAddr().String(),
						Timestamp:  time.Now().Format(time.RFC3339),
						RawData:    clipUTF8(string(buf[:n]), 1024),
					})
				}
				mu.Unlock()
			}(conn)
		}
	}()

	var triggerStatus int
	var triggerExID string
	if strings.TrimSpace(triggerURL) != "" {
		rep := func(s string) string {
			s = strings.ReplaceAll(s, "{{CALLBACK_URL}}", cbURL)
			s = strings.ReplaceAll(s, "{{CALLBACK_HOST_PORT}}", cbHostPort)
			return s
		}
		finalURL := rep(strings.TrimSpace(triggerURL))
		finalBody := rep(body)
		m := strings.ToUpper(strings.TrimSpace(method))
		if m == "" {
			m = "GET"
		}
		var br io.Reader
		if finalBody != "" {
			br = strings.NewReader(finalBody)
		}
		req, err := http.NewRequestWithContext(waitCtx, m, finalURL, br)
		if err == nil {
			for k, v := range headers {
				req.Header.Set(k, rep(v))
			}
			tr := t.getPooledTransport("", "", true)
			client := &http.Client{Transport: tr, Timeout: timeoutDur}
			if resp, err := client.Do(req); err == nil {
				rawB, _ := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
				resp.Body.Close()
				triggerStatus = resp.StatusCode
				if t != nil && t.trafficStore != nil {
					triggerExID = t.trafficStore.RecordDirect(m, finalURL, resp.Proto, req.Header, []byte(finalBody), resp.StatusCode, resp.Header, rawB)
				}
			}
		}
	}

	<-waitCtx.Done()
	_ = ln.Close()

	mu.Lock()
	defer mu.Unlock()
	return map[string]any{
		"mode":                "oast_callback",
		"callback_url":        cbURL,
		"callback_host_port":  cbHostPort,
		"listen_port":         actualPort,
		"trigger_status":      triggerStatus,
		"trigger_exchange_id": triggerExID,
		"callbacks_received":  len(hits),
		"callbacks":           hits,
	}, nil
}
