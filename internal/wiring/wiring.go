// Package wiring assembles the enhancer service from runtime config. It is
// the single assembly point shared by every entry binary (the openpe CLI and
// its hooks, openpe-server): CR-009 found the two hand-maintained copies had
// drifted — the HTTP server silently ignored OPENPE_MESSAGE_STYLE, so the
// same configuration produced different provider payloads depending on which
// entry served the request. One builder makes that class of drift structural
// nonsense.
package wiring

import (
	"log/slog"
	"strings"

	"github.com/AoManoh/openpe/internal/config"
	openacectx "github.com/AoManoh/openpe/internal/context/openace"
	"github.com/AoManoh/openpe/internal/enhancer"
)

// NewEnhancerService builds the fully configured enhancer service: optional
// Openace context provider, system prompt (style preset or explicit
// override), message style, language guard, content warnings, and logger.
// An invalid prompt style fails loudly here, at startup, matching
// enhancer.ResolveSystemPrompt's contract.
func NewEnhancerService(provider enhancer.Provider, cfg config.Config) (*enhancer.Service, error) {
	svc := enhancer.NewService(provider)
	if cfg.Openace.Enabled {
		contextProvider, err := openacectx.New(openacectx.Config{
			DaemonAddr:        cfg.Openace.Addr,
			DaemonToken:       cfg.Openace.Token,
			ProviderProfileID: cfg.Openace.ProviderProfileID,
			MaxOutputLength:   cfg.Openace.MaxOutputLength,
			Timeout:           cfg.Openace.Timeout,
			MaxRetries:        cfg.Openace.MaxRetries,
			RetryBaseDelay:    cfg.Openace.RetryBaseDelay,
			RetryMaxDelay:     cfg.Openace.RetryMaxDelay,
			RetryJitter:       cfg.Openace.RetryJitter,
		})
		if err != nil {
			return nil, err
		}
		svc = enhancer.NewServiceWithContext(provider, contextProvider)
	}
	systemPrompt, err := enhancer.ResolveSystemPrompt(cfg.SystemPrompt, cfg.PromptStyle)
	if err != nil {
		return nil, err
	}
	return svc.
		WithSystemPrompt(systemPrompt).
		WithMessageStyle(MessageStyleFromConfig(cfg)).
		WithLanguageGuard(LanguageGuardFromConfig(cfg)).
		WithContentWarnings(enhancer.ContentWarningsConfig{
			Enabled:      cfg.Warnings.Enabled,
			ExtraActions: cfg.Warnings.ExtraActions,
			NumMaxLen:    cfg.Warnings.NumMaxLen,
		}, cfg.Language).
		WithLogger(slog.Default()), nil
}

// MessageStyleFromConfig maps the config string onto the enhancer's message
// style. config.Load already normalizes the value, but directly constructed
// configs (tests, future embedders) get the same tolerant mapping. Unknown
// values fall back to flatten, keeping the historical, eval-validated layout
// the default (see config.Config.MessageStyle).
func MessageStyleFromConfig(cfg config.Config) enhancer.MessageStyle {
	switch strings.ToLower(strings.TrimSpace(cfg.MessageStyle)) {
	case "hybrid":
		return enhancer.StyleHybrid
	case "structured":
		return enhancer.StyleStructured
	default:
		return enhancer.StyleFlatten
	}
}

// LanguageGuardFromConfig maps the config-layer guard switches onto the
// enhancer's LanguageGuardConfig (keeps internal/config enhancer-free).
func LanguageGuardFromConfig(cfg config.Config) enhancer.LanguageGuardConfig {
	return enhancer.LanguageGuardConfig{
		Enabled:  cfg.LanguageGuard.Enabled,
		Reanchor: cfg.LanguageGuard.Reanchor,
	}
}
