package manual

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type Mode string

const (
	ModePreview Mode = "preview"
)

func Parse(prompt string) (string, Mode, bool) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "pe" {
		return "", ModePreview, true
	}
	rest, ok := strings.CutPrefix(prompt, "pe")
	if !ok || rest == "" {
		return prompt, "", false
	}
	separator, size := utf8.DecodeRuneInString(rest)
	if separator == ':' || separator == '：' || unicode.IsSpace(separator) {
		return strings.TrimSpace(rest[size:]), ModePreview, true
	}
	return prompt, "", false
}
