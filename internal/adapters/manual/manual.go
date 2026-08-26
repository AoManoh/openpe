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

// Parse 解析手动触发语法，返回（任务正文, 点名的规范名列表, 模式, 是否触发）。
//
// 支持三类形态：
//   - `pe` / `pe <任务>` / `pe:<任务>` / `pe：<任务>`：无规范点名，names 为 nil；
//   - `pe+名字1+名字2 <任务>`（分隔符同样允许半角/全角冒号或任意空白）：
//     显式点名规范文档，names 按出现顺序返回；
//   - 其余输入不触发，原样（trim 后）透传。
//
// 名字段只做语法切分，不做合法性判断：空名字（`pe+`、`pe++a`）原样保留在
// names 里，由 specs 加载层拒绝并报错——这里若悄悄丢弃，用户的语法笔误就会
// 被静默吞掉（违反本功能"不静默兜底"的业务契约）。
func Parse(prompt string) (string, []string, Mode, bool) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "pe" {
		return "", nil, ModePreview, true
	}
	rest, ok := strings.CutPrefix(prompt, "pe")
	if !ok || rest == "" {
		return prompt, nil, "", false
	}
	separator, size := utf8.DecodeRuneInString(rest)
	if separator == '+' {
		names, task := cutSpecNames(rest[size:])
		return task, names, ModePreview, true
	}
	if separator == ':' || separator == '：' || unicode.IsSpace(separator) {
		return strings.TrimSpace(rest[size:]), nil, ModePreview, true
	}
	return prompt, nil, "", false
}

// cutSpecNames 从 `pe+` 之后的文本中切出规范名段与任务正文。名字段延伸到
// 首个空白或冒号分隔符（半角/全角）为止，段内以 '+' 分隔多个名字。
func cutSpecNames(s string) ([]string, string) {
	end := len(s)
	sepSize := 0
	for i, r := range s {
		if r == ':' || r == '：' || unicode.IsSpace(r) {
			end = i
			sepSize = utf8.RuneLen(r)
			break
		}
	}
	names := strings.Split(s[:end], "+")
	task := strings.TrimSpace(s[end+sepSize:])
	return names, task
}
