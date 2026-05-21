package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/enhancer"
)

type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	Timeout    time.Duration
	HTTPClient *http.Client
}

type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func New(cfg Config) (*Client, error) {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		return nil, errors.New("OPENPE_BASE_URL is required")
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("invalid OPENPE_BASE_URL: %w", err)
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, errors.New("OPENPE_API_KEY is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, errors.New("OPENPE_MODEL is required")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    httpClient,
	}, nil
}

func (c *Client) Complete(ctx context.Context, req enhancer.CompletionRequest) (enhancer.CompletionResponse, error) {
	payload := chatCompletionRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		Stream: false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return enhancer.CompletionResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatCompletionsURL(), bytes.NewReader(body))
	if err != nil {
		return enhancer.CompletionResponse{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return enhancer.CompletionResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return enhancer.CompletionResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return enhancer.CompletionResponse{}, fmt.Errorf("openai-compatible provider returned HTTP %d: %s", resp.StatusCode, trimBody(data))
	}
	var decoded chatCompletionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return enhancer.CompletionResponse{}, fmt.Errorf("decode provider response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return enhancer.CompletionResponse{}, errors.New("provider response missing choices")
	}
	text := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if text == "" {
		return enhancer.CompletionResponse{}, errors.New("provider response missing message content")
	}
	model := decoded.Model
	if strings.TrimSpace(model) == "" {
		model = c.model
	}
	return enhancer.CompletionResponse{
		Text:     text,
		Provider: "openai-compatible",
		Model:    model,
	}, nil
}

func (c *Client) chatCompletionsURL() string {
	if strings.HasSuffix(c.baseURL, "/v1") {
		return c.baseURL + "/chat/completions"
	}
	return c.baseURL + "/v1/chat/completions"
}

func trimBody(data []byte) string {
	body := strings.TrimSpace(string(data))
	if len(body) > 512 {
		return body[:512] + "..."
	}
	return body
}

type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}
