package enhancer

import (
	"context"
	"strings"
)

const systemPrompt = `You are openPE, a prompt enhancement layer for coding agents.

Rewrite the user's request into a clear, actionable prompt for a coding agent.
Preserve the user's intent, constraints, language, and safety boundaries.
Keep the enhanced prompt self-contained so it remains valid when pasted into an IDE or CLI coding-agent chat.
Use only the provided history, rules, guidelines, and context.
Do not invent repository facts, file names, APIs, test results, or user decisions.
Do not rely on client-specific hidden state, prompt replacement, clipboard success, or proprietary IDE behavior.
Do not answer the task yourself.
Return only the enhanced prompt.`

type Service struct {
	provider        Provider
	contextProvider ContextProvider
}

func NewService(provider Provider) *Service {
	return &Service{provider: provider}
}

func NewServiceWithContext(provider Provider, contextProvider ContextProvider) *Service {
	return &Service{provider: provider, contextProvider: contextProvider}
}

func (s *Service) Enhance(ctx context.Context, req Request) (Response, error) {
	if s.provider == nil {
		return Response{}, providerMissingError()
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return Response{}, invalid("prompt is required")
	}
	if s.contextProvider != nil && len(req.Context.Retrieval) == 0 {
		retrieved, err := s.contextProvider.Retrieve(ctx, req)
		if err != nil {
			return Response{}, err
		}
		req.Context.Retrieval = append(req.Context.Retrieval, retrieved...)
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
