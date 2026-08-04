package delivery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/AoManoh/openpe/internal/adapters/clipboard"
	"github.com/AoManoh/openpe/internal/adapters/preview"
	"github.com/AoManoh/openpe/internal/fsatomic"
)

type Cache struct {
	PreviewPath string
	PromptPath  string
}

type Result struct {
	Method     string
	CopyError  error
	Cache      Cache
	CacheError error
}

type Options struct {
	Client    string
	Language  string
	CacheDir  string
	Clipboard *clipboard.Options
}

func Deliver(ctx context.Context, enhanced string, opts Options) Result {
	cache, cacheErr := SaveWithOptions(opts.Client, enhanced, opts.Language, opts)
	var method string
	var copyErr error
	if opts.Clipboard != nil {
		method, copyErr = clipboard.CopyWithOptions(ctx, enhanced, *opts.Clipboard)
	} else {
		method, copyErr = clipboard.Copy(ctx, enhanced)
	}
	return Result{
		Method:     method,
		CopyError:  copyErr,
		Cache:      cache,
		CacheError: cacheErr,
	}
}

func Save(client string, enhanced string, language string) (Cache, error) {
	return SaveWithOptions(client, enhanced, language, Options{})
}

func SaveWithOptions(client string, enhanced string, language string, opts Options) (Cache, error) {
	enhanced = strings.TrimSpace(enhanced)
	if enhanced == "" {
		return Cache{}, errors.New("empty enhanced prompt")
	}
	dir, err := cacheDir(client, opts.CacheDir)
	if err != nil {
		return Cache{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Cache{}, err
	}
	cache := Cache{
		PreviewPath: filepath.Join(dir, "last.md"),
		PromptPath:  filepath.Join(dir, "last-prompt.txt"),
	}
	// Atomic replacement: parallel hook flights may write this cache
	// concurrently while `hook last` (or a de-dup replay on an old binary)
	// reads it — a reader must never observe a torn prompt (CR-003).
	if err := fsatomic.WriteFile(cache.PromptPath, []byte(enhanced+"\n"), 0o600); err != nil {
		return cache, err
	}
	if err := fsatomic.WriteFile(cache.PreviewPath, []byte(preview.Markdown(enhanced, language)+"\n"), 0o600); err != nil {
		return cache, err
	}
	return cache, nil
}

func ReadLastPreview(client string) (string, error) {
	return ReadLastPreviewWithOptions(client, Options{})
}

func ReadLastPrompt(client string) (string, error) {
	return ReadLastPromptWithOptions(client, Options{})
}

func ReadLastPreviewWithOptions(client string, opts Options) (string, error) {
	return readFile(func(client string) (string, error) {
		return LastPreviewPathWithOptions(client, opts)
	}, client)
}

func ReadLastPromptWithOptions(client string, opts Options) (string, error) {
	return readFile(func(client string) (string, error) {
		return LastPromptPathWithOptions(client, opts)
	}, client)
}

func LastPreviewPath(client string) (string, error) {
	return LastPreviewPathWithOptions(client, Options{})
}

func LastPreviewPathWithOptions(client string, opts Options) (string, error) {
	dir, err := cacheDir(client, opts.CacheDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last.md"), nil
}

func LastPromptPath(client string) (string, error) {
	return LastPromptPathWithOptions(client, Options{})
}

func LastPromptPathWithOptions(client string, opts Options) (string, error) {
	dir, err := cacheDir(client, opts.CacheDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last-prompt.txt"), nil
}

func HookStatus(result Result, language string, fallbackCommand string) string {
	if result.CopyError == nil && strings.TrimSpace(result.Method) != "" {
		return successStatus(result, language)
	}
	return failureStatus(result, language, fallbackCommand)
}

func AppendHookStatus(prefix string, result Result, language string, fallbackCommand string) string {
	status := HookStatus(result, language, fallbackCommand)
	if strings.TrimSpace(prefix) == "" || result.CopyError != nil {
		return status
	}
	return strings.TrimSpace(prefix) + " " + status
}

func AppendPromptFallback(status string, prompt string, language string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return strings.TrimSpace(status)
	}
	var b strings.Builder
	if strings.TrimSpace(status) != "" {
		b.WriteString(strings.TrimSpace(status))
		b.WriteString("\n\n")
	}
	if isEnglish(language) {
		b.WriteString("Enhanced prompt follows. Copy this block if clipboard delivery failed:\n\n")
	} else {
		b.WriteString("增强提示词如下。若剪贴板未更新，可直接复制此代码块使用：\n\n")
	}
	b.WriteString("```markdown\n")
	b.WriteString(prompt)
	b.WriteString("\n```")
	return b.String()
}

func successStatus(result Result, language string) string {
	if isEnglish(language) {
		if strings.EqualFold(strings.TrimSpace(result.Method), "OSC52") {
			return "openPE blocked the original prompt and sent an OSC52 clipboard sequence. Paste, edit, and send it if your terminal supports OSC52."
		}
		return "openPE blocked the original prompt and updated the clipboard. Paste, edit, and send it."
	}
	if strings.EqualFold(strings.TrimSpace(result.Method), "OSC52") {
		return "openPE 已拦截原始消息并发送 OSC52 剪贴板序列；若终端支持，请粘贴后编辑发送。"
	}
	return "openPE 已拦截原始消息并更新剪贴板，请粘贴后编辑发送。"
}

func failureStatus(result Result, language string, fallbackCommand string) string {
	copyDetail := "unknown clipboard error"
	if result.CopyError != nil {
		copyDetail = result.CopyError.Error()
	}
	cacheDetail := ""
	if result.CacheError != nil {
		cacheDetail = result.CacheError.Error()
	}
	cached := result.Cache.PromptPath != "" && result.CacheError == nil
	if isEnglish(language) {
		var b strings.Builder
		if cached {
			b.WriteString("openPE generated and cached the enhanced prompt, but clipboard was NOT updated; do not paste existing clipboard content.")
		} else {
			b.WriteString("openPE generated the enhanced prompt, but clipboard/cache delivery failed; do not paste existing clipboard content.")
		}
		if cached {
			b.WriteString(" Open this file for paste-ready text: ")
			b.WriteString(result.Cache.PromptPath)
			b.WriteString(".")
		}
		if strings.TrimSpace(fallbackCommand) != "" && cached {
			b.WriteString(" Enhanced prompt is cached; run `")
			b.WriteString(fallbackCommand)
			b.WriteString("` to print paste-ready text.")
		} else if cacheDetail != "" {
			b.WriteString(" Enhanced prompt cache failed: ")
			b.WriteString(cacheDetail)
			b.WriteString(".")
		}
		b.WriteString(" Copy error: ")
		b.WriteString(copyDetail)
		return b.String()
	}
	var b strings.Builder
	if cached {
		b.WriteString("openPE 已生成并缓存增强提示词，但剪贴板未更新；请勿直接粘贴旧内容。")
	} else {
		b.WriteString("openPE 已生成增强提示词，但剪贴板或缓存交付失败；请勿直接粘贴旧内容。")
	}
	if cached {
		b.WriteString("可直接打开文件获取可粘贴文本：")
		b.WriteString(result.Cache.PromptPath)
		b.WriteString("。")
	}
	if strings.TrimSpace(fallbackCommand) != "" && cached {
		b.WriteString("增强结果已缓存，可运行 `")
		b.WriteString(fallbackCommand)
		b.WriteString("` 查看可粘贴文本。")
	} else if cacheDetail != "" {
		b.WriteString("增强结果缓存失败：")
		b.WriteString(cacheDetail)
		b.WriteString("。")
	}
	b.WriteString("复制错误：")
	b.WriteString(copyDetail)
	return b.String()
}

func readFile(pathFn func(string) (string, error), client string) (string, error) {
	path, err := pathFn(client)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func cacheDir(client string, override string) (string, error) {
	if value := strings.TrimSpace(override); value != "" {
		return value, nil
	}
	if value := strings.TrimSpace(os.Getenv("OPENPE_CACHE_DIR")); value != "" {
		return value, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "openpe", safeClientName(client)), nil
}

func safeClientName(client string) string {
	client = strings.TrimSpace(client)
	if client == "" {
		return "generic"
	}
	var b strings.Builder
	for _, r := range client {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteByte('-')
	}
	name := strings.Trim(b.String(), "-_")
	if name == "" {
		return "generic"
	}
	return name
}

func isEnglish(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "en-us", "english":
		return true
	default:
		return false
	}
}
