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
	Client   string
	Language string
}

func Deliver(ctx context.Context, enhanced string, opts Options) Result {
	cache, cacheErr := Save(opts.Client, enhanced, opts.Language)
	method, copyErr := clipboard.Copy(ctx, enhanced)
	return Result{
		Method:     method,
		CopyError:  copyErr,
		Cache:      cache,
		CacheError: cacheErr,
	}
}

func Save(client string, enhanced string, language string) (Cache, error) {
	enhanced = strings.TrimSpace(enhanced)
	if enhanced == "" {
		return Cache{}, errors.New("empty enhanced prompt")
	}
	dir, err := cacheDir(client)
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
	if err := os.WriteFile(cache.PromptPath, []byte(enhanced+"\n"), 0o600); err != nil {
		return cache, err
	}
	if err := os.WriteFile(cache.PreviewPath, []byte(preview.Markdown(enhanced, language)+"\n"), 0o600); err != nil {
		return cache, err
	}
	return cache, nil
}

func ReadLastPreview(client string) (string, error) {
	return readFile(LastPreviewPath, client)
}

func ReadLastPrompt(client string) (string, error) {
	return readFile(LastPromptPath, client)
}

func LastPreviewPath(client string) (string, error) {
	dir, err := cacheDir(client)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last.md"), nil
}

func LastPromptPath(client string) (string, error) {
	dir, err := cacheDir(client)
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
	if isEnglish(language) {
		var b strings.Builder
		b.WriteString("openPE blocked the original prompt, but clipboard was NOT updated; do not paste existing clipboard content.")
		if strings.TrimSpace(fallbackCommand) != "" && result.CacheError == nil {
			b.WriteString(" Enhanced prompt is cached; run `")
			b.WriteString(fallbackCommand)
			b.WriteString("` to print paste-ready text.")
		} else if result.Cache.PromptPath != "" && result.CacheError == nil {
			b.WriteString(" Enhanced prompt is cached at ")
			b.WriteString(result.Cache.PromptPath)
			b.WriteString(".")
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
	b.WriteString("openPE 已拦截原始消息；剪贴板未更新，请勿直接粘贴旧内容。")
	if strings.TrimSpace(fallbackCommand) != "" && result.CacheError == nil {
		b.WriteString("增强结果已缓存，可运行 `")
		b.WriteString(fallbackCommand)
		b.WriteString("` 查看可粘贴文本。")
	} else if result.Cache.PromptPath != "" && result.CacheError == nil {
		b.WriteString("增强结果已缓存：")
		b.WriteString(result.Cache.PromptPath)
		b.WriteString("。")
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

func cacheDir(client string) (string, error) {
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
