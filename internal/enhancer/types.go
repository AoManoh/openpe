package enhancer

import (
	"context"
	"fmt"
)

type Request struct {
	Prompt     string    `json:"prompt"`
	Client     string    `json:"client,omitempty"`
	CWD        string    `json:"cwd,omitempty"`
	Mode       string    `json:"mode,omitempty"`
	History    []Message `json:"history,omitempty"`
	Rules      []string  `json:"rules,omitempty"`
	Guidelines []string  `json:"guidelines,omitempty"`
	Context    Context   `json:"context,omitempty"`
	Options    Options   `json:"options,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Context struct {
	Files     []ContextFile `json:"files,omitempty"`
	Retrieval []string      `json:"retrieval,omitempty"`
}

type ContextFile struct {
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
}

type Options struct {
	MaxContextTokens int  `json:"max_context_tokens,omitempty"`
	ReturnMetadata   bool `json:"return_metadata,omitempty"`
}

type Response struct {
	EnhancedPrompt string   `json:"enhanced_prompt"`
	Warnings       []string `json:"warnings,omitempty"`
	Metadata       Metadata `json:"metadata,omitempty"`
}

type Metadata struct {
	UsedContext []string      `json:"used_context,omitempty"`
	Sections    []SectionInfo `json:"sections,omitempty"`
	Provider    string        `json:"provider,omitempty"`
	Model       string        `json:"model,omitempty"`
}

// MessageStyle selects how an enhancement request is laid out on the wire.
type MessageStyle int

const (
	// StyleFlatten (default) sends [system, user] where the single user message
	// embeds the conversation history as labeled "[role] content" text. This is
	// the historical, eval-validated layout.
	StyleFlatten MessageStyle = iota
	// StyleHybrid sends [system, prior user/assistant turns..., final user] where
	// prior conversation is delivered as real chat turns and only the final user
	// turn carries the rewrite instruction + original prompt. Opt-in until eval
	// A/B promotes it (OPENPE_MESSAGE_STYLE=hybrid).
	StyleHybrid
)

// CompletionRequest is the provider-facing prompt. System is always the first
// message. When Messages is non-empty it is sent verbatim after System (hybrid
// multi-turn); otherwise the single User string is sent as one user message
// (flatten). Keeping both fields preserves backward compatibility for callers
// and tests that only populate User.
type CompletionRequest struct {
	System   string
	User     string
	Messages []Message
}

type CompletionResponse struct {
	Text     string
	Provider string
	Model    string
}

type Provider interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

type ContextProvider interface {
	Retrieve(ctx context.Context, req Request) ([]string, error)
}

type ValidationError struct {
	Message string
}

func (e ValidationError) Error() string {
	return e.Message
}

func invalid(message string) error {
	return ValidationError{Message: message}
}

func providerMissingError() error {
	return fmt.Errorf("enhancer provider is required")
}
