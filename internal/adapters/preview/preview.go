package preview

import "strings"

func Markdown(enhanced string, language ...string) string {
	value := ""
	if len(language) > 0 {
		value = language[0]
	}
	return markdown(enhanced, value)
}

func markdown(enhanced string, language string) string {
	enhanced = strings.TrimSpace(enhanced)
	if isEnglish(language) {
		return strings.TrimSpace(`# openPE Enhanced Prompt

> This preview was not submitted to the model. Copy, edit, and send it manually when ready.

` + "```markdown\n" + enhanced + "\n```")
	}
	return strings.TrimSpace(`# openPE 增强提示词

> 此预览未提交给模型。请复制、编辑后再发送。

` + "```markdown\n" + enhanced + "\n```")
}

func isEnglish(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "en-us", "english":
		return true
	default:
		return false
	}
}
