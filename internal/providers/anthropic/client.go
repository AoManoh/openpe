// Package anthropic implements enhancer.Provider against the Anthropic Messages
// API (POST /v1/messages), for gateways that only speak Anthropic's protocol
// rather than the OpenAI /v1/chat/completions schema. The enhancer core is
// unaware of the wire protocol; only this package and the provider selector know.
package anthropic

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

const (
	// defaultVersion is the Anthropic API version header. Configurable because
	// some gateways pin a specific value.
	defaultVersion = "2023-06-01"
	// defaultMaxTokens caps the response length. Anthropic requires max_tokens;
	// the OpenAI schema leaves it optional, so we supply a generous default.
	defaultMaxTokens = 4096
	defaultTimeout   = 60 * time.Second
)

type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	Timeout    time.Duration
	MaxTokens  int
	Version    string
	HTTPClient *http.Client
}

type Client struct {
	baseURL   string
	apiKey    string
	model     string
	maxTokens int
	version   string
	http      *http.Client
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
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	version := strings.TrimSpace(cfg.Version)
	if version == "" {
		version = defaultVersion
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		apiKey:    apiKey,
		model:     model,
		maxTokens: maxTokens,
		version:   version,
		http:      httpClient,
	}, nil
}

func (c *Client) Complete(ctx context.Context, req enhancer.CompletionRequest) (enhancer.CompletionResponse, error) {
	messages := toAnthropicMessages(req)
	if len(messages) == 0 {
		return enhancer.CompletionResponse{}, errors.New("no user content to send")
	}
	payload := messagesRequest{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System:    strings.TrimSpace(req.System),
		Messages:  messages,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return enhancer.CompletionResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.messagesURL(), bytes.NewReader(body))
	if err != nil {
		return enhancer.CompletionResponse{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "application/json")
	// Anthropic uses x-api-key + anthropic-version, not Authorization: Bearer.
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", c.version)

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
		return enhancer.CompletionResponse{}, fmt.Errorf("anthropic provider returned HTTP %d: %s", resp.StatusCode, trimBody(data))
	}
	var decoded messagesResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return enhancer.CompletionResponse{}, fmt.Errorf("decode provider response: %w", err)
	}
	text := decoded.text()
	if text == "" {
		return enhancer.CompletionResponse{}, errors.New("provider response missing text content")
	}
	model := decoded.Model
	if strings.TrimSpace(model) == "" {
		model = c.model
	}
	return enhancer.CompletionResponse{
		Text:     text,
		Provider: "anthropic",
		Model:    model,
	}, nil
}

// toAnthropicMessages maps the canonical CompletionRequest onto Anthropic's
// messages array. Anthropic requires: at least one message, the first message is
// role "user", and roles alternate. So we keep only user/assistant turns with
// content, drop any leading assistant turn(s), and merge consecutive same-role
// turns. Flatten (single User) trivially yields one user message.
func toAnthropicMessages(req enhancer.CompletionRequest) []message {
	var raw []message
	if len(req.Messages) > 0 {
		for _, m := range req.Messages {
			role := strings.TrimSpace(m.Role)
			content := strings.TrimSpace(m.Content)
			if content == "" || (role != "user" && role != "assistant") {
				continue
			}
			raw = append(raw, message{Role: role, Content: content})
		}
	}
	if len(raw) == 0 {
		if u := strings.TrimSpace(req.User); u != "" {
			return []message{{Role: "user", Content: u}}
		}
		return nil
	}
	// Drop leading assistant turns (must start with user).
	for len(raw) > 0 && raw[0].Role != "user" {
		raw = raw[1:]
	}
	// Merge consecutive same-role turns so roles strictly alternate.
	merged := make([]message, 0, len(raw))
	for _, m := range raw {
		if n := len(merged); n > 0 && merged[n-1].Role == m.Role {
			merged[n-1].Content += "\n\n" + m.Content
			continue
		}
		merged = append(merged, m)
	}
	return merged
}

func (c *Client) messagesURL() string {
	if strings.HasSuffix(c.baseURL, "/v1") {
		return c.baseURL + "/messages"
	}
	return c.baseURL + "/v1/messages"
}

func trimBody(data []byte) string {
	body := strings.TrimSpace(string(data))
	if len(body) > 512 {
		return body[:512] + "..."
	}
	return body
}

type messagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messagesResponse struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// text concatenates the text blocks of the response, skipping non-text blocks
// (e.g. tool_use), and trims the result.
func (r messagesResponse) text() string {
	var b strings.Builder
	for _, block := range r.Content {
		if block.Type == "text" || (block.Type == "" && block.Text != "") {
			b.WriteString(block.Text)
		}
	}
	return strings.TrimSpace(b.String())
}
