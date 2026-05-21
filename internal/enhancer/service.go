package enhancer

import (
	"context"
	"strings"
)

const systemPrompt = `You are openPE, a prompt enhancement layer for coding agents.

Rewrite the user's request into a clear, actionable prompt for a coding agent.
Preserve the user's intent, constraints, language, and safety boundaries.
Use only the provided history, rules, guidelines, and context.
Do not invent repository facts, file names, APIs, test results, or user decisions.
Do not answer the task yourself.
Return only the enhanced prompt.`

type Service struct {
	provider Provider
}

func NewService(provider Provider) *Service {
	return &Service{provider: provider}
}

func (s *Service) Enhance(ctx context.Context, req Request) (Response, error) {
	if s.provider == nil {
		return Response{}, providerMissingError()
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return Response{}, invalid("prompt is required")
	}

	user, usedContext, warnings := buildUserPrompt(req)
	out, err := s.provider.Complete(ctx, CompletionRequest{
		System: systemPrompt,
		User:   user,
	})
	if err != nil {
		return Response{}, err
	}
	enhanced := strings.TrimSpace(out.Text)
	if enhanced == "" {
		return Response{}, invalid("provider returned empty enhanced prompt")
	}
	return Response{
		EnhancedPrompt: enhanced,
		Warnings:       warnings,
		Metadata: Metadata{
			UsedContext: usedContext,
			Provider:    out.Provider,
			Model:       out.Model,
		},
	}, nil
}
