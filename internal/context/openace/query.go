package openace

import (
	"fmt"
	"strings"

	"github.com/AoManoh/openpe/internal/enhancer"
)

const maxQueryFieldChars = 4000

func BuildInformationRequest(req enhancer.Request) string {
	var b strings.Builder
	b.WriteString("Find source-grounded codebase context that will help openPE rewrite the user's prompt for a coding agent.\n")
	b.WriteString("Prioritize relevant files, symbols, call chains, configuration, tests, documented project constraints, and validation commands.\n")
	b.WriteString("Return concise evidence with file paths and concrete facts. Do not solve the user's task and do not invent missing repository facts.\n")
	writeQuerySection(&b, "User prompt", req.Prompt)
	writeQuerySection(&b, "Target client", req.Client)
	writeQuerySection(&b, "Mode", req.Mode)
	writeQuerySection(&b, "Workspace", req.CWD)
	if len(req.Rules) > 0 {
		writeQueryList(&b, "Rules", req.Rules)
	}
	if len(req.Guidelines) > 0 {
		writeQueryList(&b, "Guidelines", req.Guidelines)
	}
	if len(req.Context.Files) > 0 {
		paths := make([]string, 0, len(req.Context.Files))
		for _, file := range req.Context.Files {
			path := strings.TrimSpace(file.Path)
			if path != "" {
				paths = append(paths, path)
			}
		}
		writeQueryList(&b, "Already provided file context paths", paths)
	}
	return strings.TrimSpace(b.String())
}

func writeQuerySection(b *strings.Builder, title string, value string) {
	value = trimQueryValue(value)
	if value == "" {
		return
	}
	fmt.Fprintf(b, "\n## %s\n%s\n", title, value)
}

func writeQueryList(b *strings.Builder, title string, values []string) {
	var cleaned []string
	for _, value := range values {
		value = trimQueryValue(value)
		if value == "" {
			continue
		}
		cleaned = append(cleaned, value)
	}
	if len(cleaned) == 0 {
		return
	}
	b.WriteString("\n## " + title + "\n")
	for _, value := range cleaned {
		fmt.Fprintf(b, "- %s\n", value)
	}
}

func trimQueryValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxQueryFieldChars {
		return value
	}
	return value[:maxQueryFieldChars] + "\n[openPE: query field truncated]"
}
