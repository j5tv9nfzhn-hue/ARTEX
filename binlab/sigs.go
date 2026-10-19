package binlab

import (
	"bytes"
	"fmt"
	"sort"
)

// CryptoHit is one known algorithm constant found inside the image.
type CryptoHit struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Confidence string `json:"confidence"`
	Section    string `json:"section"`
	FileOffset string `json:"file_offset"`
	RVA        string `json:"rva"`
	Context    string `json:"context"`
}

// UnknownTable is a 256-byte permutation that no signature recognised: the
// strongest hint that a custom substitution layer exists.
type UnknownTable struct {
	Section string  `json:"section"`
	RVA     string  `json:"rva"`
	Length  int     `json:"length"`
	Entropy float64 `json:"entropy"`
}

// CryptoReport is the result of a signature sweep.
type CryptoReport struct {
	Hits          []CryptoHit    `json:"hits"`
	UnknownTables []UnknownTable `json:"unmatched_tables"`
	Next          []string       `json:"next"`
}

type sigRule struct {
	ID         string
	Name       string
	Kind       string
	Confidence string
	Patterns   [][]byte
}

const maxCryptoHits = 64

// cryptoRules are matched as raw byte sequences at any offset, so they are
// independent of architecture, compiler and table relocation.
var cryptoRules = []sigRule{
	{
		ID: "aes-sbox", Name: "AES S-box", Kind: "sbox", Confidence: "high",
		Patterns: [][]byte{{
			0x63, 0x7c, 0x77, 0x7b, 0xf2, 0x6b, 0x6f, 0xc5,
			0x30, 0x01, 0x67, 0x2b, 0xfe, 0xd7, 0xab, 0x76,
		}},
	},
	{
		ID: "aes-inv-sbox", Name: "AES inverse S-box", Kind: "sbox", Confidence: "high",
		Patterns: [][]byte{{
			0x52, 0x09, 0x6a, 0xd5, 0x30, 0x36, 0xa5, 0x38,
			0xbf, 0x40, 0xa3, 0x9e, 0x81, 0xf3, 0xd7, 0xfb,
		}},
	},
	{
		ID: "aes-te0", Name: "AES T-table (Te0)", Kind: "sbox", Confidence: "medium",
		Patterns: [][]byte{{
			0xa5, 0x63, 0x63, 0xc6, 0x84, 0x7c, 0x7c, 0xf8,
		}},
	},
	{
		ID: "sm4-sbox", Name: "SM4 S-box", Kind: "sbox", Confidence: "high",
		Patterns: [][]byte{{
			0xd6, 0x90, 0xe9, 0xfe, 0xcc, 0xe1, 0x3d, 0xb7,
			0x16, 0xb6, 0x14, 0xc2, 0x28, 0xfb, 0x2c, 0x05,
		}},
	},
	{
		ID: "crc32-ieee", Name: "CRC-32 (IEEE) table", Kind: "table", Confidence: "high",
		Patterns: [][]byte{{
			0x00, 0x00, 0x00, 0x00, 0x96, 0x30, 0x07, 0x77,
			0x2c, 0x61, 0x0e, 0xee, 0xba, 0x51, 0x09, 0x99,
		}},
	},
	{
		ID: "sha1-iv", Name: "SHA-1 initial vector", Kind: "iv", Confidence: "high",
		Patterns: [][]byte{{
			0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
			0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10,
			0xf0, 0xe1, 0xd2, 0xc3,
		}},
	},
	{
		ID: "md5-iv", Name: "MD5 initial vector", Kind: "iv", Confidence: "high",
		Patterns: [][]byte{{
			0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
			0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10,
		}},
	},
	{
		ID: "sha256-k", Name: "SHA-256 round constants", Kind: "constant", Confidence: "high",
		Patterns: [][]byte{{
			0x98, 0x2f, 0x8a, 0x42, 0x91, 0x44, 0x37, 0x71,
			0xcf, 0xfb, 0xc0, 0xb5,
		}},
	},
	{
		ID: "sm3-iv", Name: "SM3 initial vector", Kind: "iv", Confidence: "medium",
		Patterns: [][]byte{{
			0x6f, 0x16, 0x80, 0x73, 0xb9, 0xb2, 0x14, 0x49,
		}},
	},
	{
		ID: "tea-delta", Name: "TEA/XTEA/XXTEA delta", Kind: "constant", Confidence: "medium",
		Patterns: [][]byte{
			{0xb9, 0x79, 0x37, 0x9e},
			{0x9e, 0x37, 0x79, 0xb9},
		},
	},
	{
		ID: "blowfish-p", Name: "Blowfish P-array", Kind: "table", Confidence: "medium",
		Patterns: [][]byte{{
			0x88, 0x6a, 0x3f, 0x24, 0xd3, 0x08, 0xa3, 0x85,
		}},
	},
	{
		ID: "chacha-sigma", Name: "ChaCha/Salsa20 sigma", Kind: "constant", Confidence: "high",
		Patterns: [][]byte{[]byte("expand 32-byte k")},
	},
	{
		ID: "bcrypt-magic", Name: "bcrypt magic", Kind: "constant", Confidence: "high",
		Patterns: [][]byte{[]byte("OrpheanBeholderScryDoubt")},
	},
	{
		ID: "base64-std", Name: "Base64 alphabet (standard)", Kind: "encoding", Confidence: "high",
		Patterns: [][]byte{[]byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/")},
	},
	{
		ID: "base64-url", Name: "Base64 alphabet (URL-safe)", Kind: "encoding", Confidence: "high",
		Patterns: [][]byte{[]byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_")},
	},
}

// Crypto sweeps every section for known algorithm constants and reports
// un-recognised 256-byte permutation tables as well.
func Crypto(img *Image) *CryptoReport {
	r := &CryptoReport{Hits: []CryptoHit{}, UnknownTables: []UnknownTable{}}
	for _, s := range img.Sections {
		body, ok := sliceRaw(img.data, s.RawOffset, s.RawSize)
		if !ok || len(body) < 4 {
			continue
		}
		for _, rule := range cryptoRules {
			if len(r.Hits) >= maxCryptoHits {
				break
			}
			for _, pat := range rule.Patterns {
				idx := bytes.Index(body, pat)
				if idx < 0 {
					continue
				}
				r.Hits = append(r.Hits, CryptoHit{
					ID:         rule.ID,
					Name:       rule.Name,
					Kind:       rule.Kind,
					Confidence: rule.Confidence,
					Section:    s.Name,
					FileOffset: fmt.Sprintf("0x%x", s.RawOffset+int64(idx)),
					RVA:        fmt.Sprintf("0x%x", s.RVA+uint64(idx)),
					Context:    fmt.Sprintf("%d-byte pattern at %s+0x%x", len(pat), s.Name, idx),
				})
				break
			}
		}
	}
	r.Hits = dropShadowedDigests(r.Hits)
	sort.SliceStable(r.Hits, func(i, j int) bool {
		if r.Hits[i].Section != r.Hits[j].Section {
			return r.Hits[i].Section < r.Hits[j].Section
		}
		return r.Hits[i].FileOffset < r.Hits[j].FileOffset
	})
	r.UnknownTables = findUnknownTables(img, r.Hits)

	if len(r.Hits) > 0 {
		r.Next = append(r.Next,
			"对命中地址做引用确认：常量存在不等于被调用。",
			"用标准测试向量在运行时自证：同样的输入必须产出同样的输出。",
		)
	}
	if len(r.UnknownTables) > 0 {
		r.Next = append(r.Next,
			"存在未识别的 256 字节置换表：按自定义算法处理，先记录结构与变换性质，不要硬套标准算法名。",
		)
	}
	return r
}

// The MD5 initial vector is a prefix of the SHA-1 initial vector. When both
// match at the same place, report only the longer one.
func dropShadowedDigests(hits []CryptoHit) []CryptoHit {
	if len(hits) < 2 {
		return hits
	}
	sha1 := make(map[string]bool, len(hits))
	for _, h := range hits {
		if h.ID == "sha1-iv" {
			sha1[h.Section+"|"+h.FileOffset] = true
		}
	}
	out := hits[:0]
	for _, h := range hits {
		if h.ID == "md5-iv" && sha1[h.Section+"|"+h.FileOffset] {
			continue
		}
		out = append(out, h)
	}
	return out
}

// findUnknownTables looks for 256-byte windows that are exact permutations of
// 0..255. Windows are tested at 256-byte alignment only, which keeps the cost
// linear and still catches compiler-emitted tables at their natural alignment.
func findUnknownTables(img *Image, known []CryptoHit) []UnknownTable {
	blocked := make(map[string]bool, len(known))
	for _, h := range known {
		blocked[h.Section+"|"+h.FileOffset] = true
	}
	var out []UnknownTable
	for _, s := range img.Sections {
		body, ok := sliceRaw(img.data, s.RawOffset, s.RawSize)
		if !ok || len(body) < 256 {
			continue
		}
		if len(body) > 2<<20 {
			body = body[:2<<20]
		}
		for off := 0; off+256 <= len(body); off += 256 {
			if blocked[fmt.Sprintf("%s|0x%x", s.Name, s.RawOffset+int64(off))] {
				continue
			}
			w := body[off : off+256]
			if !isPermutation(w) {
				continue
			}
			out = append(out, UnknownTable{
				Section: s.Name,
				RVA:     fmt.Sprintf("0x%x", s.RVA+uint64(off)),
				Length:  256,
				Entropy: Shannon(w),
			})
			if len(out) >= 8 {
				return out
			}
		}
	}
	return out
}

func isPermutation(b []byte) bool {
	var seen [256]bool
	for _, c := range b {
		if seen[c] {
			return false
		}
		seen[c] = true
	}
	for _, v := range seen {
		if !v {
			return false
		}
	}
	return true
}
