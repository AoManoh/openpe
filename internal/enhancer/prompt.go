package enhancer

import (
	"fmt"
	"strings"
)

const charsPerTokenApprox = 4

func buildUserPrompt(req Request) (string, []string, []string) {
	var b strings.Builder
	var used []string
	var warnings []string

	writeSection(&b, "Original prompt", req.Prompt)
	if value := strings.TrimSpace(req.Client); value != "" {
		writeSection(&b, "Target client", value)
	}
	if value := strings.TrimSpace(req.Mode); value != "" {
		writeSection(&b, "Mode", value)
	}
	if value := strings.TrimSpace(req.CWD); value != "" {
		writeSection(&b, "Workspace", value)
	}
	writeList(&b, "Enhancement contract", compatibilityGuidance(req))
	if len(req.Rules) > 0 {
		used = append(used, "rules")
		writeList(&b, "Rules", req.Rules)
	}
	if len(req.Guidelines) > 0 {
		used = append(used, "guidelines")
		writeList(&b, "Guidelines", req.Guidelines)
	}
	if len(req.History) > 0 {
		used = append(used, "history")
		writeHistory(&b, req.History)
	}
	if len(req.Context.Files) > 0 {
		used = append(used, "context.files")
		writeFiles(&b, req.Context.Files)
	}
	if len(req.Context.Retrieval) > 0 {
		used = append(used, "context.retrieval")
		writeList(&b, "Retrieved context", req.Context.Retrieval)
	}

	b.WriteString("\nRewrite the original prompt now. Return only the enhanced prompt.\n")

	user := b.String()
	if req.Options.MaxContextTokens > 0 {
		limit := req.Options.MaxContextTokens * charsPerTokenApprox
		if len(user) > limit {
			user = user[:limit] + "\n\n[openPE warning: context was truncated to fit max_context_tokens]\n"
			warnings = append(warnings, "context truncated to max_context_tokens")
		}
	}
	return user, used, warnings
}

func compatibilityGuidance(req Request) []string {
	client := normalizeLabel(req.Client)
	mode := normalizeLabel(req.Mode)
	lines := []string{
		"Preserve the user's original intent, language, explicit constraints, and safety boundaries.",
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

func writeSection(b *strings.Builder, title string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	fmt.Fprintf(b, "\n## %s\n%s\n", title, value)
}

func writeList(b *strings.Builder, title string, values []string) {
	b.WriteString("\n## " + title + "\n")
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		fmt.Fprintf(b, "- %s\n", value)
	}
}

func writeHistory(b *strings.Builder, history []Message) {
	b.WriteString("\n## Conversation history\n")
	for _, msg := range history {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "unknown"
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		fmt.Fprintf(b, "[%s] %s\n", role, content)
	}
}

func writeFiles(b *strings.Builder, files []ContextFile) {
	b.WriteString("\n## Context files\n")
	for _, file := range files {
		content := strings.TrimSpace(file.Content)
		if content == "" {
			continue
		}
		path := strings.TrimSpace(file.Path)
		if path == "" {
			path = "unnamed"
		}
		fmt.Fprintf(b, "### %s\n%s\n", path, content)
	}
}
