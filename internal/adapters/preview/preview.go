package preview

import "strings"

func Markdown(enhanced string) string {
	enhanced = strings.TrimSpace(enhanced)
	return strings.TrimSpace(`# openPE Enhanced Prompt

> This preview was not submitted to the model. Copy, edit, and send it manually when ready.

` + "```markdown\n" + enhanced + "\n```")
}
