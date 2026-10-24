package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Repo 是本仓库自己的发布源，也是**唯一**的更新源。
//
// 两条硬约束，改之前先读完：
//  1. 不回落到任何上游/分支仓库。本产品是独立发行版，版本号与上游并不同步，
//     混用上游源会把别人的包换进本产品；早先写死上游作者的仓库，那个仓库后来
//     在公网 404，所有客户端的更新链路就静默全断了。
//  2. 不在运行时改。更新源可配等于给任何能改设置的人一条远程代码执行通道，
//     对一个渗透测试平台来说这个口子开不得——只允许构建期注入：
//     -ldflags "-X github.com/Autumn-27/artex/selfupdate.Repo=owner/name"
var Repo = "j5tv9nfzhn-hue/ARTEX-HYDRA"

// repoPattern 只接受 owner/name，杜绝把 scheme/host 拼进来绕过下面的域名白名单。
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func init() {
	if !repoPattern.MatchString(Repo) {
		panic("selfupdate: 非法的更新源 Repo=" + Repo + "（应为 owner/name）")
	}
}

// LatestURL 是 GitHub 的"最新正式版"接口。它会自动跳过 prerelease 和 draft。
func LatestURL() string { return "https://api.github.com/repos/" + Repo + "/releases/latest" }

// ManifestName 是每个 Release 都带的固定名清单，由 .github/workflows/release.yml 生成。
const ManifestName = "latest.json"

// ManifestURL 走 github.com 的网页别名 /releases/latest/download/<文件名>。
//
// 这条 URL 不是 API 调用，**不消耗**未认证的 60 次/小时配额，也不需要先知道 tag
// 是什么。日常检测走它，额度开销为 0；清单缺失（老版本、替换中途）才回落到 API。
func ManifestURL() string {
	return "https://github.com/" + Repo + "/releases/latest/download/" + ManifestName
}

// allowedHosts 限定升级链路能访问的域名。配合下面的 checkRedirect，
// 任何一跳被重定向到名单外的主机都会直接失败——这是防止 DNS 污染 / 中间人
// 把二进制换掉的第一道闸门，第二道是 SHA256SUMS 比对。
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true, // release 资产实际落地的对象存储
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com":            true,
}

// Release 是 GitHub Release 里我们关心的字段。
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset 是 Release 上挂的一个文件。
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
	// SHA256 是发布清单（latest.json）里随资产一起给出的摘要。
	// 它与 SHA256SUMS 是两条独立通道，取到两者就做交叉校验（见 stage.go）。
	SHA256 string `json:"sha256,omitempty"`
}

// NewClient 构造一个只认 GitHub 域名的 HTTP 客户端。proxy 为空则直连。
//
// 刻意不复用默认 Transport：升级链路必须强制走 TLS 且校验证书，不能被别处
// 设置的 InsecureSkipVerify 之类影响到。
func NewClient(proxy string) *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Minute, // 下载整包，不能按请求级超时卡死
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("重定向次数过多")
			}
			return checkURL(req.URL)
		},
	}
}

// checkURL 强制 https + 域名白名单。
func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("拒绝非 HTTPS 地址：%s", u.Scheme+"://"+u.Host)
	}
	if !allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("拒绝非 GitHub 域名地址：%s", u.Hostname())
	}
	return nil
}

// FetchLatest 查询最新正式版。
func FetchLatest(ctx context.Context, c *http.Client) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, LatestURL(), nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "artex-selfupdate")

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接 GitHub（可在系统设置中配置全局代理）：%w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusTooManyRequests:
		// 未认证的 GitHub API 是每 IP 每小时 60 次，共用出口 IP 时很容易撞上。
		return nil, fmt.Errorf("GitHub API 触发请求限流（每小时 60 次），请稍后重试")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("仓库 %s 尚无正式发布版本", Repo)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub 响应码 %d", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析 Release 失败：%w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, fmt.Errorf("Release 缺少 tag")
	}
	return &rel, nil
}

// FetchLatestManifest 用 0 配额的网页别名取发布清单。
//
// 清单是 release.yml 在发布时生成的固定名资产（latest.json），字段与 Release 同构：
//
//	{"tag_name":"v0.3.16","body":"...","html_url":"...","assets":[{"name":"...","size":123}]}
//
// 之所以优先它而不是 REST API：未认证 API 是每 IP 每小时 60 次，公司内网、NAT
// 出口、共用代理后面几十台机器会一起撞墙；而网页别名与资产下载都不计入配额。
func FetchLatestManifest(ctx context.Context, c *http.Client) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ManifestURL(), nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-selfupdate")

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接 GitHub（可在系统设置中配置全局代理）：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("发布清单 %s 不可用（HTTP %d）", ManifestName, resp.StatusCode)
	}
	// 清单只有几百字节；限长读，避免被异常响应灌满内存。
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析发布清单失败：%w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, fmt.Errorf("发布清单缺少 tag")
	}
	// 清单里没写下载地址时，按 GitHub Release 的固定命名拼出来。
	for i := range rel.Assets {
		if strings.TrimSpace(rel.Assets[i].URL) == "" {
			rel.Assets[i].URL = "https://github.com/" + Repo + "/releases/download/" +
				rel.TagName + "/" + rel.Assets[i].Name
		}
	}
	return &rel, nil
}

// FetchLatestWithFallback 是检测的统一入口：先清单，失败再回落到 REST API。
//
// 回落只针对同一个 Repo，绝不会去问上游仓库——"换源"必须是重新发版的事，
// 不能由网络状况或响应内容决定。
func FetchLatestWithFallback(ctx context.Context, c *http.Client) (*Release, error) {
	rel, mErr := FetchLatestManifest(ctx, c)
	if mErr == nil {
		return rel, nil
	}
	rel, aErr := FetchLatest(ctx, c)
	if aErr == nil {
		return rel, nil
	}
	return nil, fmt.Errorf("%w（清单通道：%v）", aErr, mErr)
}

// AssetName 返回当前平台对应的发布包名，与 build.sh 的 package_binary 保持一致：
// artex-rea-<版本>-<os>-<arch>.zip（版本号不带 v 前缀）。
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("artex-rea-%s-%s-%s.zip", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// FindAsset 在 Release 里按名字找资产。
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}
