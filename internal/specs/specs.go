// Package specs 实现"显式点名加载用户自定义 prompt 规范"的加载与追加层。
//
// 业务契约见 docs/requirements/2026-08-25-keyword-prompt-specs.md：用户在
// `pe+<名字> <任务>` 中显式点名 ~/.config/openpe/specs/<名字>.md，openPE 在
// 增强前加载校验（失败即阻断，绝不静默降级），增强成功后把规范原文逐字
// 追加到结果后段（a1 机械追加，规范不进入增强模型请求）。
//
// 本包刻意不做任何宽松回退：找不到、读不出、非 UTF-8、内容为空、超过上限
// 都返回结构化 *LoadError，由调用方按客户端语言渲染并阻断本次增强。这与
// config.systemPromptFromEnv 的"读不出就静默用默认"是相反的设计选择——
// 那里的回退对象是内置默认值，这里被丢弃的将是用户点名要求的约束。
package specs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// DefaultDirName 是用户级配置目录下的规范子目录名：~/.config/openpe/specs。
	DefaultDirName = "specs"
	// DefaultMaxChars 是单份规范的默认字符上限；超限报错而不截断。
	DefaultMaxChars = 8000
	// FileExtension 是规范文档的固定扩展名；文件名词干即规范名。
	FileExtension = ".md"
)

// Spec 是一份加载成功的规范：Name 为点名用的名字，Content 为归一化后的
// 原文（已剥 UTF-8 BOM、换行统一为 LF、去除首尾空白）。
type Spec struct {
	Name    string
	Content string
}

// Reason 枚举加载失败原因，供测试断言与未来结构化上报使用。
type Reason string

const (
	ReasonInvalidName Reason = "invalid_name"
	ReasonNotFound    Reason = "not_found"
	ReasonReadError   Reason = "read_error"
	ReasonInvalidUTF8 Reason = "invalid_utf8"
	ReasonEmpty       Reason = "empty"
	ReasonOversize    Reason = "oversize"
)

// LoadError 是单个规范加载失败的结构化描述。加载遵循全有或全无：任一名字
// 失败即返回该错误并放弃整批，调用方必须阻断本次增强。
type LoadError struct {
	Name      string
	Reason    Reason
	Dir       string
	Detail    string   // read_error 时的底层错误原文
	Available []string // not_found 时目录中现有的规范名（尽力提供）
	Size      int      // oversize 时的实际字符数
	Limit     int      // oversize 时的上限
}

// Error 实现 error 接口，默认中文（与 config.DefaultLanguage 一致）。
func (e *LoadError) Error() string { return e.Message("") }

// Message 按客户端语言渲染用户可见错误文案，说明哪个名字、什么原因、
// 以及用户下一步怎么改。
func (e *LoadError) Message(language string) string {
	if isEnglish(language) {
		switch e.Reason {
		case ReasonInvalidName:
			return "invalid spec name \"" + e.Name + "\": names must be non-empty, must not start with a dot, and must not contain path separators, colons, or control characters."
		case ReasonNotFound:
			msg := "spec \"" + e.Name + "\" not found in " + e.Dir + "."
			if len(e.Available) > 0 {
				msg += " Available specs: " + strings.Join(e.Available, ", ") + "."
			} else {
				msg += " The directory has no spec documents yet."
			}
			return msg
		case ReasonReadError:
			return "failed to read spec \"" + e.Name + "\": " + e.Detail
		case ReasonInvalidUTF8:
			return "spec \"" + e.Name + "\" is not valid UTF-8 text; please re-save it as UTF-8."
		case ReasonEmpty:
			return "spec \"" + e.Name + "\" is empty."
		case ReasonOversize:
			return "spec \"" + e.Name + "\" exceeds the size limit (" + itoa(e.Size) + "/" + itoa(e.Limit) + " characters); openPE never truncates specs, please trim the document."
		}
		return "failed to load spec \"" + e.Name + "\"."
	}
	switch e.Reason {
	case ReasonInvalidName:
		return "规范名「" + e.Name + "」不合法：名字不能为空、不能以点开头、不能包含路径分隔符、冒号或控制字符。"
	case ReasonNotFound:
		msg := "规范「" + e.Name + "」未找到（目录 " + e.Dir + "）。"
		if len(e.Available) > 0 {
			msg += "现有规范：" + strings.Join(e.Available, "、") + "。"
		} else {
			msg += "该目录还没有任何规范文档。"
		}
		return msg
	case ReasonReadError:
		return "规范「" + e.Name + "」读取失败：" + e.Detail
	case ReasonInvalidUTF8:
		return "规范「" + e.Name + "」不是有效的 UTF-8 文本，请将文件另存为 UTF-8 编码。"
	case ReasonEmpty:
		return "规范「" + e.Name + "」内容为空。"
	case ReasonOversize:
		return "规范「" + e.Name + "」超过大小上限（" + itoa(e.Size) + "/" + itoa(e.Limit) + " 字符）；openPE 不会截断规范，请精简文档。"
	}
	return "规范「" + e.Name + "」加载失败。"
}

// DefaultDir 解析默认规范目录 ~/.config/openpe/specs，与 hook 安装器使用的
// 用户级配置目录（~/.config/openpe/.env）同根。
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "openpe", DefaultDirName), nil
}

// ValidateName 校验点名的规范名。名字来自用户输入并会拼接进文件路径，
// 必须在拼接前拒绝路径穿越与畸形输入：空名、以点开头（含 "." ".." 与隐藏
// 文件）、路径分隔符、冒号（Windows 盘符/ADS）、控制字符。中文等 Unicode
// 字母数字、连字符、下划线均合法。
func ValidateName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || r == ':' || r == '：' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Load 按点名顺序解析规范：每个名字先校验、再直接读取 dir/<name>.md 并做
// 归一化（剥 BOM、统一 LF、Trim）与内容校验。maxChars <= 0 时使用
// DefaultMaxChars。任一名字失败立即返回 *LoadError（全有或全无）。
func Load(dir string, names []string, maxChars int) ([]Spec, *LoadError) {
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}
	loaded := make([]Spec, 0, len(names))
	for _, name := range names {
		if !ValidateName(name) {
			return nil, &LoadError{Name: name, Reason: ReasonInvalidName, Dir: dir}
		}
		data, err := os.ReadFile(filepath.Join(dir, name+FileExtension))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, &LoadError{Name: name, Reason: ReasonNotFound, Dir: dir, Available: availableNames(dir)}
			}
			return nil, &LoadError{Name: name, Reason: ReasonReadError, Dir: dir, Detail: err.Error()}
		}
		content, ok := normalize(data)
		if !ok {
			return nil, &LoadError{Name: name, Reason: ReasonInvalidUTF8, Dir: dir}
		}
		if content == "" {
			return nil, &LoadError{Name: name, Reason: ReasonEmpty, Dir: dir}
		}
		if size := utf8.RuneCountInString(content); size > maxChars {
			return nil, &LoadError{Name: name, Reason: ReasonOversize, Dir: dir, Size: size, Limit: maxChars}
		}
		loaded = append(loaded, Spec{Name: name, Content: content})
	}
	return loaded, nil
}

// LoadWithDefaults 是 hook/CLI 接线用的便捷入口：names 为空直接返回 nil；
// dir 为空回退到 DefaultDir()。返回的 error 要么是 *LoadError，要么是
// 用户主目录解析失败——两者都必须阻断本次增强，调用方用 ErrorMessage
// 渲染本地化文案。
func LoadWithDefaults(dir string, names []string, maxChars int) ([]Spec, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(dir) == "" {
		resolved, err := DefaultDir()
		if err != nil {
			return nil, err
		}
		dir = resolved
	}
	loaded, lerr := Load(dir, names, maxChars)
	if lerr != nil {
		return nil, lerr
	}
	return loaded, nil
}

// ErrorMessage 把 LoadWithDefaults/Load 的错误渲染成用户可见文案：
// *LoadError 走本地化 Message，其余错误（如主目录解析失败）带原文外抛。
func ErrorMessage(err error, language string) string {
	var lerr *LoadError
	if errors.As(err, &lerr) {
		return lerr.Message(language)
	}
	if isEnglish(language) {
		return "failed to load user specs: " + err.Error()
	}
	return "加载用户规范失败：" + err.Error()
}

// Append 把加载成功的规范逐字追加到增强结果后段（a1 交付形态）：
//
//	<增强正文>
//
//	[用户规范：三问]
//	<原文>
//
// 块头随客户端语言本地化；多份按点名顺序排列。无规范时原样返回。
func Append(enhanced string, loaded []Spec, language string) string {
	if len(loaded) == 0 {
		return enhanced
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(enhanced, "\n"))
	for _, s := range loaded {
		b.WriteString("\n\n")
		b.WriteString(blockHeader(s.Name, language))
		b.WriteString("\n")
		b.WriteString(s.Content)
	}
	return b.String()
}

// Names 返回已应用规范名列表，供交付层的可观测文案使用。
func Names(loaded []Spec) []string {
	names := make([]string, 0, len(loaded))
	for _, s := range loaded {
		names = append(names, s.Name)
	}
	return names
}

func blockHeader(name string, language string) string {
	if isEnglish(language) {
		return "[User spec: " + name + "]"
	}
	return "[用户规范：" + name + "]"
}

// normalize 剥离 UTF-8 BOM、统一换行为 LF、去除首尾空白；非法 UTF-8 返回
// ok=false（不猜测转码，交由调用方报错）。
func normalize(data []byte) (string, bool) {
	data = bytesTrimBOM(data)
	if !utf8.Valid(data) {
		return "", false
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text), true
}

func bytesTrimBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

// availableNames 在 not_found 的错误路径上尽力列出目录中现有的规范名，帮助
// 用户快速改正点名；列目录本身失败时返回 nil——主错误（not_found）仍然
// 完整外抛，这里只是附加信息缺失，不构成静默兜底。
func availableNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		base := entry.Name()
		if !strings.HasSuffix(base, FileExtension) {
			continue
		}
		name := strings.TrimSuffix(base, FileExtension)
		if ValidateName(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func isEnglish(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "en-us", "english":
		return true
	default:
		return false
	}
}

// itoa 避免为一个整数格式化引入 fmt 依赖。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
