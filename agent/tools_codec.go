package agent

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec
	"crypto/rand"
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/url"
	"strconv"
	"strings"

	actool "github.com/Autumn-27/norma/tool"
	"github.com/golang-jwt/jwt/v5"
)

func (t *ToolSet) codecCryptoTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        "codec_crypto",
		Description: "原生编解码、哈希摘要、对称加解密与 JWT 构造/破解工具（替代 python -c 编解码与加解密脚本）。支持 Base64/Base64URL/Hex/URL/HTML/Unicode/Gzip/Zlib 编解码，MD5/SHA1/SHA256/SHA512/HMAC 计算，XOR、AES-CBC/ECB/GCM 加解密，以及 JWT 解析(jwt_decode)、伪造/重签名(jwt_forge / jwt_sign)、alg=none 攻击构造(jwt_none)与弱口令爆破(jwt_crack)。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"operation": map[string]any{
					"type":        "string",
					"description": "操作类型：base64_encode | base64_decode | base64url_encode | base64url_decode | hex_encode | hex_decode | url_encode | url_decode | url_encode_all | html_escape | html_unescape | html_encode | html_decode | unicode_escape | unicode_unescape | gzip_compress | gzip_decompress | zlib_compress | zlib_decompress | md5 | sha1 | sha256 | sha512 | hmac_sha1 | hmac_sha256 | hmac_sha512 | xor_hex | xor_base64 | aes_cbc_encrypt | aes_cbc_decrypt | aes_ecb_encrypt | aes_ecb_decrypt | aes_gcm_encrypt | aes_gcm_decrypt | jwt_decode | jwt_forge | jwt_sign | jwt_none | jwt_crack",
				},
				"action":        map[string]any{"type": "string", "description": "operation 的兼容别名"},
				"input":         map[string]any{"type": "string", "description": "输入文本、密文（Base64/Hex）或 JWT Token"},
				"input_format":  map[string]any{"type": "string", "description": "输入数据编码格式：raw（默认）| hex | base64"},
				"output_format": map[string]any{"type": "string", "description": "输出格式（加解密适用）：base64（默认）| hex | raw"},
				"key":           map[string]any{"type": "string", "description": "HMAC/XOR/AES/JWT 密钥"},
				"key_format":    map[string]any{"type": "string", "description": "密钥编码格式：raw（默认）| hex | base64"},
				"iv":            map[string]any{"type": "string", "description": "AES-CBC IV 或 AES-GCM Nonce"},
				"iv_format":     map[string]any{"type": "string", "description": "IV/Nonce 编码格式：raw（默认）| hex | base64"},
				"jwt_claims":    map[string]any{"type": "object", "description": "jwt_forge / jwt_sign / jwt_none 时要写入或覆盖的 Claims 键值对"},
				"jwt_header":    map[string]any{"type": "object", "description": "jwt_forge / jwt_sign / jwt_none 时自定义 Header 字段（如 kid、jku、x5u）"},
				"jwt_alg":       map[string]any{"type": "string", "description": "jwt_forge / jwt_sign 算法：HS256（默认）| HS384 | HS512 | none"},
				"wordlist":      map[string]any{"type": "array", "description": "jwt_crack 额外追加的候选密钥列表", "items": map[string]any{"type": "string"}},
			},
		},
		ReadOnly:    func(json.RawMessage) bool { return true },
		Permissions: allowAllPerm,
		Run: func(_ context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Operation    string         `json:"operation"`
				Action       string         `json:"action"`
				Input        string         `json:"input"`
				InputFormat  string         `json:"input_format"`
				OutputFormat string         `json:"output_format"`
				Key          string         `json:"key"`
				KeyFormat    string         `json:"key_format"`
				IV           string         `json:"iv"`
				IVFormat     string         `json:"iv_format"`
				JWTClaims    map[string]any `json:"jwt_claims"`
				JWTHeader    map[string]any `json:"jwt_header"`
				JWTAlg       string         `json:"jwt_alg"`
				Wordlist     []string       `json:"wordlist"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			op := strings.ToLower(firstNonEmpty(a.Operation, a.Action))
			if op == "" {
				return actool.Errorf("operation 为必填参数"), nil
			}
			rawBytes, err := decodeFormattedBytes(a.Input, a.InputFormat)
			if err != nil {
				return actool.Errorf("解析 input 失败: " + err.Error()), nil
			}
			keyBytes, err := decodeFormattedBytes(a.Key, a.KeyFormat)
			if err != nil {
				return actool.Errorf("解析 key 失败: " + err.Error()), nil
			}
			ivBytes, err := decodeFormattedBytes(a.IV, a.IVFormat)
			if err != nil {
				return actool.Errorf("解析 iv 失败: " + err.Error()), nil
			}

			resMap := map[string]any{"operation": op}
			switch op {
			case "base64_encode":
				resMap["output"] = base64.StdEncoding.EncodeToString(rawBytes)
			case "base64_decode":
				dec, err := decodeBase64Flexible(a.Input)
				if err != nil {
					return actool.Errorf("Base64 解码失败: " + err.Error()), nil
				}
				resMap["output"] = string(dec)
				resMap["output_hex"] = hex.EncodeToString(dec)
			case "base64url_encode":
				resMap["output"] = base64.RawURLEncoding.EncodeToString(rawBytes)
			case "base64url_decode":
				dec, err := decodeBase64Flexible(a.Input)
				if err != nil {
					return actool.Errorf("Base64URL 解码失败: " + err.Error()), nil
				}
				resMap["output"] = string(dec)
				resMap["output_hex"] = hex.EncodeToString(dec)
			case "hex_encode":
				resMap["output"] = hex.EncodeToString(rawBytes)
			case "hex_decode":
				clean := strings.ReplaceAll(strings.TrimSpace(a.Input), " ", "")
				clean = strings.TrimPrefix(strings.TrimPrefix(clean, "0x"), "0X")
				dec, err := hex.DecodeString(clean)
				if err != nil {
					return actool.Errorf("Hex 解码失败: " + err.Error()), nil
				}
				resMap["output"] = string(dec)
			case "url_encode":
				resMap["output"] = url.QueryEscape(a.Input)
			case "url_encode_all":
				var sb strings.Builder
				for i := 0; i < len(rawBytes); i++ {
					fmt.Fprintf(&sb, "%%%02X", rawBytes[i])
				}
				resMap["output"] = sb.String()
			case "url_decode":
				u, err := url.QueryUnescape(a.Input)
				if err != nil {
					return actool.Errorf("URL 解码失败: " + err.Error()), nil
				}
				resMap["output"] = u
			case "html_escape", "html_encode":
				resMap["output"] = html.EscapeString(a.Input)
			case "html_unescape", "html_decode":
				resMap["output"] = html.UnescapeString(a.Input)
			case "unicode_escape":
				q := strconv.QuoteToASCII(a.Input)
				resMap["output"] = q[1 : len(q)-1]
			case "unicode_unescape":
				uq, err := strconv.Unquote(`"` + strings.ReplaceAll(a.Input, `"`, `\"`) + `"`)
				if err != nil {
					return actool.Errorf("Unicode 反转义失败: " + err.Error()), nil
				}
				resMap["output"] = uq
			case "gzip_compress":
				var buf bytes.Buffer
				gw := gzip.NewWriter(&buf)
				_, _ = gw.Write(rawBytes)
				_ = gw.Close()
				resMap["output_base64"] = base64.StdEncoding.EncodeToString(buf.Bytes())
				resMap["output_hex"] = hex.EncodeToString(buf.Bytes())
			case "gzip_decompress":
				compBytes := rawBytes
				if a.InputFormat == "" {
					if b, err := decodeBase64Flexible(a.Input); err == nil {
						compBytes = b
					}
				}
				gr, err := gzip.NewReader(bytes.NewReader(compBytes))
				if err != nil {
					return actool.Errorf("Gzip 解压失败: " + err.Error()), nil
				}
				dec, err := io.ReadAll(io.LimitReader(gr, 4*1024*1024))
				_ = gr.Close()
				if err != nil {
					return actool.Errorf("Gzip 读取失败: " + err.Error()), nil
				}
				resMap["output"] = string(dec)
			case "zlib_compress":
				var buf bytes.Buffer
				zw := zlib.NewWriter(&buf)
				_, _ = zw.Write(rawBytes)
				_ = zw.Close()
				resMap["output_base64"] = base64.StdEncoding.EncodeToString(buf.Bytes())
				resMap["output_hex"] = hex.EncodeToString(buf.Bytes())
			case "zlib_decompress":
				compBytes := rawBytes
				if a.InputFormat == "" {
					if b, err := decodeBase64Flexible(a.Input); err == nil {
						compBytes = b
					}
				}
				zr, err := zlib.NewReader(bytes.NewReader(compBytes))
				if err != nil {
					return actool.Errorf("Zlib 解压失败: " + err.Error()), nil
				}
				dec, err := io.ReadAll(io.LimitReader(zr, 4*1024*1024))
				_ = zr.Close()
				if err != nil {
					return actool.Errorf("Zlib 读取失败: " + err.Error()), nil
				}
				resMap["output"] = string(dec)
			case "md5":
				s := md5.Sum(rawBytes) //nolint:gosec
				resMap["output_hex"] = hex.EncodeToString(s[:])
				resMap["output_base64"] = base64.StdEncoding.EncodeToString(s[:])
			case "sha1":
				s := sha1.Sum(rawBytes) //nolint:gosec
				resMap["output_hex"] = hex.EncodeToString(s[:])
				resMap["output_base64"] = base64.StdEncoding.EncodeToString(s[:])
			case "sha256":
				s := sha256.Sum256(rawBytes)
				resMap["output_hex"] = hex.EncodeToString(s[:])
				resMap["output_base64"] = base64.StdEncoding.EncodeToString(s[:])
			case "sha512":
				s := sha512.Sum512(rawBytes)
				resMap["output_hex"] = hex.EncodeToString(s[:])
				resMap["output_base64"] = base64.StdEncoding.EncodeToString(s[:])
			case "hmac_sha1":
				mac := hmac.New(sha1.New, keyBytes)
				mac.Write(rawBytes)
				sum := mac.Sum(nil)
				resMap["output_hex"] = hex.EncodeToString(sum)
				resMap["output_base64"] = base64.StdEncoding.EncodeToString(sum)
			case "hmac_sha256":
				mac := hmac.New(sha256.New, keyBytes)
				mac.Write(rawBytes)
				sum := mac.Sum(nil)
				resMap["output_hex"] = hex.EncodeToString(sum)
				resMap["output_base64"] = base64.StdEncoding.EncodeToString(sum)
			case "hmac_sha512":
				mac := hmac.New(sha512.New, keyBytes)
				mac.Write(rawBytes)
				sum := mac.Sum(nil)
				resMap["output_hex"] = hex.EncodeToString(sum)
				resMap["output_base64"] = base64.StdEncoding.EncodeToString(sum)
			case "xor_hex", "xor_base64":
				if len(keyBytes) == 0 {
					return actool.Errorf("XOR 运算必须提供非空 key"), nil
				}
				inData := rawBytes
				if op == "xor_hex" && a.InputFormat == "" {
					if d, err := hex.DecodeString(strings.TrimSpace(a.Input)); err == nil {
						inData = d
					}
				} else if op == "xor_base64" && a.InputFormat == "" {
					if d, err := decodeBase64Flexible(a.Input); err == nil {
						inData = d
					}
				}
				outBytes := make([]byte, len(inData))
				for i := range inData {
					outBytes[i] = inData[i] ^ keyBytes[i%len(keyBytes)]
				}
				resMap["output"] = string(outBytes)
				resMap["output_hex"] = hex.EncodeToString(outBytes)
				resMap["output_base64"] = base64.StdEncoding.EncodeToString(outBytes)
			case "aes_cbc_encrypt":
				block, err := aes.NewCipher(keyBytes)
				if err != nil {
					return actool.Errorf("AES 密钥长度无效(需 16/24/32 字节): " + err.Error()), nil
				}
				if len(ivBytes) == 0 {
					ivBytes = make([]byte, aes.BlockSize)
					_, _ = rand.Read(ivBytes)
				}
				if len(ivBytes) != aes.BlockSize {
					return actool.Errorf("AES-CBC IV 长度必须为 16 字节"), nil
				}
				padded := pkcs7Pad(rawBytes, aes.BlockSize)
				ct := make([]byte, len(padded))
				cipher.NewCBCEncrypter(block, ivBytes).CryptBlocks(ct, padded)
				resMap["iv_hex"] = hex.EncodeToString(ivBytes)
				resMap["output"] = formatBytesOut(ct, a.OutputFormat)
			case "aes_cbc_decrypt":
				block, err := aes.NewCipher(keyBytes)
				if err != nil {
					return actool.Errorf("AES 密钥长度无效(需 16/24/32 字节): " + err.Error()), nil
				}
				if len(ivBytes) != aes.BlockSize {
					return actool.Errorf("AES-CBC IV 长度必须为 16 字节"), nil
				}
				ct, err := decodeCiphertextInput(a.Input, a.InputFormat, rawBytes)
				if err != nil {
					return actool.Errorf("密文解析失败: " + err.Error()), nil
				}
				if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
					return actool.Errorf("AES-CBC 密文长度必须是 16 字节的整数倍"), nil
				}
				pt := make([]byte, len(ct))
				cipher.NewCBCDecrypter(block, ivBytes).CryptBlocks(pt, ct)
				unpadded, err := pkcs7Unpad(pt, aes.BlockSize)
				if err != nil {
					return actool.Errorf("PKCS#7 去填充失败(密钥或 IV 错误): " + err.Error()), nil
				}
				resMap["output"] = string(unpadded)
				resMap["output_hex"] = hex.EncodeToString(unpadded)
			case "aes_ecb_encrypt":
				block, err := aes.NewCipher(keyBytes)
				if err != nil {
					return actool.Errorf("AES 密钥长度无效(需 16/24/32 字节): " + err.Error()), nil
				}
				padded := pkcs7Pad(rawBytes, aes.BlockSize)
				ct := make([]byte, len(padded))
				for bs := 0; bs < len(padded); bs += aes.BlockSize {
					block.Encrypt(ct[bs:bs+aes.BlockSize], padded[bs:bs+aes.BlockSize])
				}
				resMap["output"] = formatBytesOut(ct, a.OutputFormat)
			case "aes_ecb_decrypt":
				block, err := aes.NewCipher(keyBytes)
				if err != nil {
					return actool.Errorf("AES 密钥长度无效(需 16/24/32 字节): " + err.Error()), nil
				}
				ct, err := decodeCiphertextInput(a.Input, a.InputFormat, rawBytes)
				if err != nil {
					return actool.Errorf("密文解析失败: " + err.Error()), nil
				}
				if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
					return actool.Errorf("AES-ECB 密文长度必须是 16 字节的整数倍"), nil
				}
				pt := make([]byte, len(ct))
				for bs := 0; bs < len(ct); bs += aes.BlockSize {
					block.Decrypt(pt[bs:bs+aes.BlockSize], ct[bs:bs+aes.BlockSize])
				}
				unpadded, err := pkcs7Unpad(pt, aes.BlockSize)
				if err != nil {
					return actool.Errorf("PKCS#7 去填充失败: " + err.Error()), nil
				}
				resMap["output"] = string(unpadded)
				resMap["output_hex"] = hex.EncodeToString(unpadded)
			case "aes_gcm_encrypt":
				block, err := aes.NewCipher(keyBytes)
				if err != nil {
					return actool.Errorf("AES 密钥长度无效(需 16/24/32 字节): " + err.Error()), nil
				}
				gcm, err := cipher.NewGCM(block)
				if err != nil {
					return actool.Errorf("初始化 GCM 失败: " + err.Error()), nil
				}
				if len(ivBytes) == 0 {
					ivBytes = make([]byte, gcm.NonceSize())
					_, _ = rand.Read(ivBytes)
				}
				if len(ivBytes) != gcm.NonceSize() {
					return actool.Errorf(fmt.Sprintf("AES-GCM Nonce 长度需为 %d 字节", gcm.NonceSize())), nil
				}
				ct := gcm.Seal(nil, ivBytes, rawBytes, nil)
				resMap["nonce_hex"] = hex.EncodeToString(ivBytes)
				resMap["output"] = formatBytesOut(ct, a.OutputFormat)
			case "aes_gcm_decrypt":
				block, err := aes.NewCipher(keyBytes)
				if err != nil {
					return actool.Errorf("AES 密钥长度无效(需 16/24/32 字节): " + err.Error()), nil
				}
				gcm, err := cipher.NewGCM(block)
				if err != nil {
					return actool.Errorf("初始化 GCM 失败: " + err.Error()), nil
				}
				if len(ivBytes) != gcm.NonceSize() {
					return actool.Errorf(fmt.Sprintf("AES-GCM Nonce 长度需为 %d 字节", gcm.NonceSize())), nil
				}
				ct, err := decodeCiphertextInput(a.Input, a.InputFormat, rawBytes)
				if err != nil {
					return actool.Errorf("密文解析失败: " + err.Error()), nil
				}
				pt, err := gcm.Open(nil, ivBytes, ct, nil)
				if err != nil {
					return actool.Errorf("AES-GCM 解密认证失败: " + err.Error()), nil
				}
				resMap["output"] = string(pt)
			case "jwt_decode":
				parts := strings.Split(strings.TrimSpace(a.Input), ".")
				if len(parts) < 2 {
					return actool.Errorf("无效的 JWT 格式（应为 header.payload[.signature]）"), nil
				}
				hdrBytes, err1 := base64.RawURLEncoding.DecodeString(parts[0])
				payBytes, err2 := base64.RawURLEncoding.DecodeString(parts[1])
				if err1 != nil || err2 != nil {
					return actool.Errorf("JWT Base64URL 解码失败"), nil
				}
				var hdrObj, payObj any
				_ = json.Unmarshal(hdrBytes, &hdrObj)
				_ = json.Unmarshal(payBytes, &payObj)
				resMap["header"] = hdrObj
				resMap["payload"] = payObj
				if len(parts) >= 3 {
					resMap["signature_b64url"] = parts[2]
				}
				if len(keyBytes) > 0 {
					tok, verr := jwt.Parse(strings.TrimSpace(a.Input), func(token *jwt.Token) (any, error) {
						return keyBytes, nil
					})
					resMap["signature_valid"] = (verr == nil && tok != nil && tok.Valid)
					if verr != nil {
						resMap["verify_error"] = verr.Error()
					}
				}
			case "jwt_none":
				claims := extractAndMergeClaims(a.Input, a.JWTClaims)
				hdr := map[string]any{"alg": "none", "typ": "JWT"}
				for k, v := range a.JWTHeader {
					hdr[k] = v
				}
				hb, _ := json.Marshal(hdr)
				pb, _ := json.Marshal(claims)
				unsigned := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb) + "."
				hdrNoneUpper := map[string]any{"alg": "None", "typ": "JWT"}
				for k, v := range a.JWTHeader {
					hdrNoneUpper[k] = v
				}
				hb2, _ := json.Marshal(hdrNoneUpper)
				unsignedUpper := base64.RawURLEncoding.EncodeToString(hb2) + "." + base64.RawURLEncoding.EncodeToString(pb) + "."
				resMap["token_alg_none"] = unsigned
				resMap["token_alg_None"] = unsignedUpper
			case "jwt_forge", "jwt_sign":
				algName := strings.ToUpper(strings.TrimSpace(a.JWTAlg))
				if algName == "NONE" || (op == "jwt_forge" && len(keyBytes) == 0 && algName == "") {
					claims := extractAndMergeClaims(a.Input, a.JWTClaims)
					hdr := map[string]any{"alg": "none", "typ": "JWT"}
					for k, v := range a.JWTHeader {
						hdr[k] = v
					}
					hb, _ := json.Marshal(hdr)
					pb, _ := json.Marshal(claims)
					unsigned := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb) + "."
					resMap["token"] = unsigned
					resMap["alg"] = "none"
					break
				}
				claims := extractAndMergeClaims(a.Input, a.JWTClaims)
				var method jwt.SigningMethod = jwt.SigningMethodHS256
				switch algName {
				case "HS384":
					method = jwt.SigningMethodHS384
				case "HS512":
					method = jwt.SigningMethodHS512
				}
				tok := jwt.NewWithClaims(method, jwt.MapClaims(claims))
				for k, v := range a.JWTHeader {
					tok.Header[k] = v
				}
				signed, err := tok.SignedString(keyBytes)
				if err != nil {
					return actool.Errorf("JWT 签名失败: " + err.Error()), nil
				}
				resMap["token"] = signed
				resMap["alg"] = method.Alg()
			case "jwt_crack":
				tokenStr := strings.TrimSpace(a.Input)
				if strings.Count(tokenStr, ".") != 2 {
					return actool.Errorf("jwt_crack 需要完整的三段式 JWT Token 在 input 中"), nil
				}
				candidates := defaultJWTWeakSecrets()
				candidates = append(candidates, a.Wordlist...)
				foundKey := ""
				tested := 0
				seen := make(map[string]struct{}, len(candidates))
				for _, cand := range candidates {
					if _, dup := seen[cand]; dup {
						continue
					}
					seen[cand] = struct{}{}
					tested++
					tok, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
						return []byte(cand), nil
					})
					if err == nil && tok != nil && tok.Valid {
						foundKey = cand
						break
					}
				}
				resMap["tested_keys"] = tested
				resMap["cracked"] = foundKey != ""
				if foundKey != "" {
					resMap["secret"] = foundKey
				}
			default:
				return actool.Errorf("未知的 codec_crypto 操作类型: " + op), nil
			}
			b, _ := json.MarshalIndent(resMap, "", "  ")
			return actool.Text(actool.Capture(tc, string(b))), nil
		},
	})
}

func decodeFormattedBytes(val, format string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "raw", "utf8", "string":
		return []byte(val), nil
	case "hex":
		clean := strings.ReplaceAll(strings.TrimSpace(val), " ", "")
		clean = strings.TrimPrefix(strings.TrimPrefix(clean, "0x"), "0X")
		return hex.DecodeString(clean)
	case "base64", "b64":
		return decodeBase64Flexible(val)
	default:
		return []byte(val), nil
	}
}

func decodeCiphertextInput(rawStr, inputFormat string, rawBytes []byte) ([]byte, error) {
	f := strings.ToLower(strings.TrimSpace(inputFormat))
	if f == "hex" || f == "base64" || f == "b64" {
		return rawBytes, nil
	}
	if b, err := decodeBase64Flexible(rawStr); err == nil && len(b) > 0 {
		return b, nil
	}
	return hex.DecodeString(strings.TrimSpace(rawStr))
}

func formatBytesOut(data []byte, format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "hex":
		return hex.EncodeToString(data)
	case "raw":
		return string(data)
	default:
		return base64.StdEncoding.EncodeToString(data)
	}
}

func decodeBase64Flexible(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("无效的 Base64 字符串")
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padLen := blockSize - (len(data) % blockSize)
	pad := bytes.Repeat([]byte{byte(padLen)}, padLen)
	return append(data, pad...)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("密文长度不是块大小(%d)的整数倍", blockSize)
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > blockSize || padLen > len(data) {
		return nil, fmt.Errorf("无效的 PKCS#7 padding")
	}
	for i := len(data) - padLen; i < len(data); i++ {
		if data[i] != byte(padLen) {
			return nil, fmt.Errorf("PKCS#7 padding 校验失败")
		}
	}
	return data[:len(data)-padLen], nil
}

func extractAndMergeClaims(tokenOrJSON string, extra map[string]any) map[string]any {
	out := make(map[string]any)
	s := strings.TrimSpace(tokenOrJSON)
	if s != "" {
		if strings.HasPrefix(s, "{") {
			_ = json.Unmarshal([]byte(s), &out)
		} else if parts := strings.Split(s, "."); len(parts) >= 2 {
			if pb, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
				_ = json.Unmarshal(pb, &out)
			}
		}
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func defaultJWTWeakSecrets() []string {
	return []string{
		"", "secret", "Secret", "SECRET", "password", "123456", "12345678", "123456789",
		"admin", "root", "jwt", "jwt_secret", "jwt-secret", "jwtsecret", "key", "private",
		"default", "changeme", "test", "dev", "development", "production", "prod",
		"supersecret", "mysecret", "your-256-bit-secret", "shhhhh", "qwerty", "letmein",
		"token", "auth", "app_secret", "api_secret", "secretkey", "secret_key",
	}
}
