package enhancer

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const charsPerTokenApprox = 4

type promptSection struct {
	name        string
	content     string
	usedContext string
	required    bool
}

func buildUserPrompt(req Request) (string, []string, []string, []SectionInfo) {
	sections := promptSections(req)
	user, infos, warnings := assemblePrompt(sections, req.Options.MaxContextTokens)
	used := usedContexts(sections, infos)
	return user, used, warnings, infos
}

func promptSections(req Request) []promptSection {
	var sections []promptSection

	sections = appendSection(sections, sectionOriginalPrompt, formatSection("Original prompt", req.Prompt), "", true)
	sections = appendSection(sections, sectionTargetClient, formatSection("Target client", req.Client), "", true)
	sections = appendSection(sections, sectionMode, formatSection("Mode", req.Mode), "", true)
	sections = appendSection(sections, sectionWorkspace, formatSection("Workspace", req.CWD), "", true)
	sections = appendSection(sections, sectionEnhancementContract, formatList("Enhancement contract", compatibilityGuidance(req)), "", true)
	sections = appendSection(sections, sectionRules, formatList("Rules", req.Rules), "rules", false)
	sections = appendSection(sections, sectionGuidelines, formatList("Guidelines", req.Guidelines), "guidelines", false)
	sections = appendSection(sections, sectionHistory, formatHistory(req.History), "history", false)
	sections = appendSection(sections, sectionContextFiles, formatFiles(req.Context.Files), "context.files", false)
	sections = appendSection(sections, sectionContextRetrieval, formatList("Retrieved context", req.Context.Retrieval), "context.retrieval", false)
	sections = appendSection(sections, sectionFinalInstruction, "\nRewrite the original prompt now. Return only the enhanced prompt.\n", "", true)

	return sections
}

func appendSection(sections []promptSection, name string, content string, usedContext string, required bool) []promptSection {
	if content == "" {
		return sections
	}
	return append(sections, promptSection{
		name:        name,
		content:     content,
		usedContext: usedContext,
		required:    required,
	})
}

func assemblePrompt(sections []promptSection, maxContextTokens int) (string, []SectionInfo, []string) {
	var b strings.Builder
	var infos []SectionInfo
	var warnings []string

	limit := 0
	remaining := 0
	if maxContextTokens > 0 {
		limit = maxContextTokens * charsPerTokenApprox
		remaining = limit - requiredLength(sections)
		if remaining < 0 {
			warnings = append(warnings, "max_context_tokens is smaller than required prompt sections; preserved original prompt and enhancement contract")
		}
	}

	truncatedContext := false
	for _, section := range sections {
		content := section.content
		truncated := false

		if limit > 0 && !section.required {
			switch {
			case remaining <= 0:
				content = ""
				truncated = true
			case runeLen(content) > remaining:
				content = truncateRunes(content, remaining)
				truncated = true
				remaining = 0
			default:
				remaining -= runeLen(content)
			}
			if truncated {
				truncatedContext = true
			}
		}

		if content != "" {
			b.WriteString(content)
		}
		infos = append(infos, SectionInfo{
			Name:      section.name,
			Length:    runeLen(content),
			Truncated: truncated,
		})
	}

	if truncatedContext {
		warnings = append(warnings, "context truncated to max_context_tokens")
	}

	return b.String(), infos, warnings
}

func usedContexts(sections []promptSection, infos []SectionInfo) []string {
	included := make(map[string]bool, len(infos))
	for _, info := range infos {
		if info.Length > 0 {
			included[info.Name] = true
		}
	}

	seen := make(map[string]bool)
	var used []string
	for _, section := range sections {
		if section.usedContext == "" || !included[section.name] || seen[section.usedContext] {
			continue
		}
		used = append(used, section.usedContext)
		seen[section.usedContext] = true
	}
	return used
}

func requiredLength(sections []promptSection) int {
	total := 0
	for _, section := range sections {
		if section.required {
			total += runeLen(section.content)
		}
	}
	return total
}

func runeLen(value string) int {
	return utf8.RuneCountInString(value)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if runeLen(value) <= limit {
		return value
	}
	var b strings.Builder
	b.Grow(limit)
	count := 0
	for _, r := range value {
		if count >= limit {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

func compatibilityGuidance(req Request) []string {
	client := normalizeLabel(req.Client)
	mode := normalizeLabel(req.Mode)
	lines := []string{
		"Preserve the user's original intent, language, explicit constraints, and any safety limits the user themselves stated; faithfully restate the request rather than refusing, watering it down, or inverting it (you only rewrite text for the user's own project — you do not execute it and cannot read file contents, so reading or using the project's own .env / config files is ordinary development to enhance normally).",
		"Return a self-contained prompt that can be pasted into a coding-agent chat or passed through a CLI adapter without relying on hidden context.",
		"Do not assume the host can replace the user's input, append private context, keep clipboard state fresh, or interpret client-specific slash commands.",
		"Make the output actionable for an agent: clarify scope, expected investigation or implementation steps, and reasonable verification when those are relevant.",
	}
	if isIDEClient(client) || isIDEMode(mode) {
		lines = append(lines, "For IDE coding-agent environments such as Windsurf, Cursor, VS Code, Composer, or Cascade, make the enhanced prompt paste-ready and robust when delivered through clipboard or cache fallback.")
	}
	if client == "codex" && mode == "agent" {
		lines = append(lines, "For Codex agent mode, keep the prompt suitable for a terminal coding agent while preserving workspace scope and validation expectations.")
	}
	return lines
}

func normalizeLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, " ", "-")
	return value
}

func isIDEClient(client string) bool {
	switch client {
	case "windsurf", "cursor", "vscode", "vs-code", "visual-studio-code", "composer", "cascade":
		return true
	default:
		return false
	}
}

func isIDEMode(mode string) bool {
	switch mode {
	case "ide", "chat", "composer", "cascade":
		return true
	default:
		return false
	}
}

func formatSection(title string, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return fmt.Sprintf("\n## %s\n%s\n", title, value)
}

func formatList(title string, values []string) string {
	var items []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			items = append(items, value)
		}
	}
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## " + title + "\n")
	for _, value := range items {
		fmt.Fprintf(&b, "- %s\n", value)
	}
	return b.String()
}

func formatHistory(history []Message) string {
	var b strings.Builder
	b.WriteString("\n## Conversation history\n")
	wrote := false
	for _, msg := range history {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "unknown"
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s] %s\n", role, content)
		wrote = true
	}
	if !wrote {
		return ""
	}
	return b.String()
}

func formatFiles(files []ContextFile) string {
	var b strings.Builder
	b.WriteString("\n## Context files\n")
	wrote := false
	for _, file := range files {
		content := strings.TrimSpace(file.Content)
		if content == "" {
			continue
		}
		path := strings.TrimSpace(file.Path)
		if path == "" {
			path = "unnamed"
		}
		fmt.Fprintf(&b, "### %s\n%s\n", path, content)
		wrote = true
	}
	if !wrote {
		return ""
	}
	return b.String()
}
