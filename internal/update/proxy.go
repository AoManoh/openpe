// Package update 实现 openpe 的新版本检查与升级支撑（业务契约
// docs/requirements/2026-08-25-version-and-update.md 方案 U）：
//
//   - 通过 Go module proxy 的 /@latest 端点查询最新发布版本（尊重用户
//     GOPROXY——国内镜像用户自动走自己的镜像，无 GitHub API 配额问题）；
//   - 把检查结果写入本地缓存文件并限频，hook 披露行只读缓存判断是否提醒，
//     增强关键路径永不发起网络请求；
//   - 版本比较基于 semver，永不建议降级；无法判定（devel）时抑制提醒。
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/mod/module"
)

// ModulePath 是本项目的模块路径，@latest 查询与 go install 目标都由它派生。
const ModulePath = "github.com/AoManoh/openpe"

// DefaultProxyBaseURL 是 GOPROXY 完全不可用（未设置且 go env 读取失败）时的
// 官方回退。
const DefaultProxyBaseURL = "https://proxy.golang.org"

// fetchTimeout 约束单次 /@latest 请求：检查是可有可无的辅助能力，宁可放弃
// 也不能拖慢 update 命令或后台刷新进程。
const fetchTimeout = 5 * time.Second

// LatestInfo 是 module proxy /@latest 端点的 JSON 结构。
type LatestInfo struct {
	Version string    `json:"Version"`
	Time    time.Time `json:"Time"`
}

// Client 查询 module proxy。BaseURL 为空时按 GOPROXY 解析；HTTPClient 为空时
// 使用带超时的默认客户端。测试用 httptest 地址注入 BaseURL。
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// Latest 查询模块的最新发布版本。零 tag 的模块会得到 main 的伪版本——这也是
// 合法结果，由调用方的 semver 比较决定是否构成"新版本"。
func (c Client) Latest(ctx context.Context, modulePath string) (LatestInfo, error) {
	escaped, err := module.EscapePath(modulePath)
	if err != nil {
		return LatestInfo{}, fmt.Errorf("escape module path %q: %w", modulePath, err)
	}
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = ResolveProxyBaseURL()
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: fetchTimeout}
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+escaped+"/@latest", nil)
	if err != nil {
		return LatestInfo{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return LatestInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return LatestInfo{}, fmt.Errorf("module proxy %s returned %s for %s/@latest", base, resp.Status, modulePath)
	}
	var info LatestInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return LatestInfo{}, fmt.Errorf("decode @latest response: %w", err)
	}
	if strings.TrimSpace(info.Version) == "" {
		return LatestInfo{}, fmt.Errorf("module proxy returned empty version")
	}
	return info, nil
}

// ResolveProxyBaseURL 解析实际要查询的 proxy 地址：环境变量 GOPROXY 优先，
// 未设置时读 `go env GOPROXY`（best-effort），再取列表中第一个可用条目；
// "off"/"direct" 不提供 /@latest 端点，跳过；全不可用回退官方 proxy。
func ResolveProxyBaseURL() string {
	return proxyBaseFromList(goproxyValue())
}

// goproxyValue 可在测试中替换，隔离对真实环境与 go 命令的依赖。环境变量
// 直接命中时不 spawn 子进程；否则读 `go env GOPROXY`（它会综合 go/env 文件）。
var goproxyValue = func() string {
	if env := strings.TrimSpace(os.Getenv("GOPROXY")); env != "" {
		return env
	}
	out, err := exec.Command("go", "env", "GOPROXY").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func proxyBaseFromList(goproxy string) string {
	for _, entry := range strings.FieldsFunc(goproxy, func(r rune) bool { return r == ',' || r == '|' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" || entry == "off" || entry == "direct" {
			continue
		}
		return strings.TrimRight(entry, "/")
	}
	return DefaultProxyBaseURL
}
