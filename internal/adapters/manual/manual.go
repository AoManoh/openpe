package manual

import "strings"

type Mode string

const (
	ModePreview Mode = "preview"
	ModeInject  Mode = "inject"
)

func Parse(prompt string) (string, Mode, bool) {
	prompt = strings.TrimSpace(prompt)
	for _, trigger := range []struct {
		prefix string
		mode   Mode
	}{
		{prefix: "pe!:", mode: ModeInject},
		{prefix: "pe！：", mode: ModeInject},
		{prefix: "pe!：", mode: ModeInject},
		{prefix: "pe！:", mode: ModeInject},
		{prefix: "openpe!:", mode: ModeInject},
		{prefix: "openpe！：", mode: ModeInject},
		{prefix: "openpe!：", mode: ModeInject},
		{prefix: "openpe！:", mode: ModeInject},
		{prefix: "增强!:", mode: ModeInject},
		{prefix: "增强！：", mode: ModeInject},
		{prefix: "增强!：", mode: ModeInject},
		{prefix: "增强！:", mode: ModeInject},
		{prefix: "pe:", mode: ModePreview},
		{prefix: "pe：", mode: ModePreview},
		{prefix: "openpe:", mode: ModePreview},
		{prefix: "openpe：", mode: ModePreview},
		{prefix: "增强:", mode: ModePreview},
		{prefix: "增强：", mode: ModePreview},
	} {
		if strings.HasPrefix(prompt, trigger.prefix) {
			return strings.TrimSpace(strings.TrimPrefix(prompt, trigger.prefix)), trigger.mode, true
		}
	}
	return prompt, "", false
}
