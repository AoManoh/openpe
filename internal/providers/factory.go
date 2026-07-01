// Package providers selects the wire protocol for the enhancer's model provider.
// It is the single place that maps a protocol-neutral Spec onto a concrete
// enhancer.Provider (OpenAI-compatible or Anthropic), so cmd entrypoints stay
// protocol-agnostic and the enhancer core never learns about HTTP schemas.
package providers

import (
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/enhancer"
	"github.com/AoManoh/openpe/internal/providers/anthropic"
	"github.com/AoManoh/openpe/internal/providers/openai"
)

// Spec is the protocol-neutral provider configuration. Provider selects the wire
// protocol; the rest are shared connection fields. MaxTokens is only consumed by
// providers that require it (Anthropic).
type Spec struct {
	Provider  string
	BaseURL   string
	APIKey    string
	Model     string
	Timeout   time.Duration
	MaxTokens int
}

// New builds the enhancer.Provider for the spec's protocol. Unknown/empty
// Provider defaults to the OpenAI-compatible client, preserving prior behaviour.
func New(s Spec) (enhancer.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(s.Provider)) {
	case "anthropic", "claude", "messages":
		return anthropic.New(anthropic.Config{
			BaseURL:   s.BaseURL,
			APIKey:    s.APIKey,
			Model:     s.Model,
			Timeout:   s.Timeout,
			MaxTokens: s.MaxTokens,
		})
	default:
		return openai.New(openai.Config{
			BaseURL: s.BaseURL,
			APIKey:  s.APIKey,
			Model:   s.Model,
			Timeout: s.Timeout,
		})
	}
}
