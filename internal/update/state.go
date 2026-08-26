package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// stateFileName 是检查结果缓存文件名，落在交付缓存根（与 delivery 包同一
// 根解析约定：override → OPENPE_CACHE_DIR → os.UserCacheDir()/openpe），
// 位于根而非客户端子目录——版本检查是跨客户端的全局状态。
const stateFileName = "update-check.json"

// CheckState 是新版本检查的本地缓存：hook 披露行只读它，永不发网络请求。
type CheckState struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// StatePath 解析缓存文件路径。override 对应 OPENPE_CACHE_DIR/Delivery.CacheDir
// 的缓存根覆盖语义。
func StatePath(override string) (string, error) {
	if value := strings.TrimSpace(override); value != "" {
		return filepath.Join(value, stateFileName), nil
	}
	if value := strings.TrimSpace(os.Getenv("OPENPE_CACHE_DIR")); value != "" {
		return filepath.Join(value, stateFileName), nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "openpe", stateFileName), nil
}

// LoadState 读取缓存；不存在或损坏返回 ok=false——缓存缺席不是错误，只是
// "尚无检查结果"（提醒静默，业务契约 U2.2）。
func LoadState(path string) (CheckState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CheckState{}, false
	}
	var state CheckState
	if err := json.Unmarshal(data, &state); err != nil {
		return CheckState{}, false
	}
	if strings.TrimSpace(state.Latest) == "" || state.CheckedAt.IsZero() {
		return CheckState{}, false
	}
	return state, true
}

// SaveState 原子写入缓存（临时文件 + rename），避免并发 hook 读到半截 JSON。
func SaveState(path string, state CheckState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Fresh 判断缓存是否仍在限频窗口内：新鲜则后台刷新跳过（不发网络请求）。
func (s CheckState) Fresh(now time.Time, interval time.Duration) bool {
	if interval <= 0 {
		return false
	}
	return now.Sub(s.CheckedAt) < interval
}

// IsNewer 判断 latest 是否严格新于 current。任一方不是合法 semver（如归一
// 后的 "devel"）返回 false——无法判定时既不提醒也不宣称"已是最新"，交由
// 调用方按场景措辞。伪版本是合法的 semver 预发布，可正常参与比较。
func IsNewer(latest string, current string) bool {
	if !semver.IsValid(latest) || !semver.IsValid(current) {
		return false
	}
	return semver.Compare(latest, current) > 0
}

// NoticeVersion 决定 hook 披露行是否提醒：缓存存在、仍新鲜、且缓存的 latest
// 严格新于当前版本时返回该版本。其余情况（无缓存、过期、devel 当前版本、
// 已是最新）一律静默——提醒缺席不是失败。
func NoticeVersion(state CheckState, ok bool, current string, now time.Time, interval time.Duration) (string, bool) {
	if !ok || !state.Fresh(now, interval) {
		return "", false
	}
	if !IsNewer(state.Latest, current) {
		return "", false
	}
	return state.Latest, true
}
