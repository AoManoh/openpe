package openace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/enhancer"
)

const (
	DefaultDaemonAddr      = "127.0.0.1:8765"
	DefaultMaxOutputLength = 12000
	DefaultTimeout         = 30 * time.Second
	DefaultMaxRetries      = 2
	DefaultRetryBaseDelay  = 250 * time.Millisecond
	DefaultRetryMaxDelay   = 2 * time.Second
	DefaultRetryJitter     = 100 * time.Millisecond
)

type Config struct {
	DaemonAddr        string
	DaemonToken       string
	ProviderProfileID string
	MaxOutputLength   int
	Timeout           time.Duration
	MaxRetries        int
	RetryBaseDelay    time.Duration
	RetryMaxDelay     time.Duration
	RetryJitter       time.Duration
	HTTPClient        *http.Client
}

type Client struct {
	baseURL           string
	token             string
	providerProfileID string
	maxOutputLength   int
	maxRetries        int
	retryBaseDelay    time.Duration
	retryMaxDelay     time.Duration
	retryJitter       time.Duration
	http              *http.Client
}

type retrieveRequest struct {
	DirectoryPath      string `json:"directory_path"`
	ProviderProfileID  string `json:"provider_profile_id,omitempty"`
	InformationRequest string `json:"information_request"`
	MaxOutputLength    int    `json:"max_output_length,omitempty"`
}

type retrieveResponse struct {
	Text              string `json:"text,omitempty"`
	ProviderProfileID string `json:"provider_profile_id,omitempty"`
	CheckpointID      string `json:"checkpoint_id,omitempty"`
	FileCount         int    `json:"file_count,omitempty"`
	Uploaded          int    `json:"uploaded,omitempty"`
	Added             int    `json:"added,omitempty"`
	Deleted           int    `json:"deleted,omitempty"`
}

func (r *retrieveResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		Text                    string `json:"text"`
		TextLegacy              string `json:"Text"`
		ProviderProfileID       string `json:"provider_profile_id"`
		ProviderProfileIDLegacy string `json:"ProviderProfileID"`
		CheckpointID            string `json:"checkpoint_id"`
		CheckpointIDLegacy      string `json:"CheckpointID"`
		FileCount               int    `json:"file_count"`
		FileCountLegacy         int    `json:"FileCount"`
		Uploaded                int    `json:"uploaded"`
		UploadedLegacy          int    `json:"Uploaded"`
		Added                   int    `json:"added"`
		AddedLegacy             int    `json:"Added"`
		Deleted                 int    `json:"deleted"`
		DeletedLegacy           int    `json:"Deleted"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Text = firstNonEmpty(raw.Text, raw.TextLegacy)
	r.ProviderProfileID = firstNonEmpty(raw.ProviderProfileID, raw.ProviderProfileIDLegacy)
	r.CheckpointID = firstNonEmpty(raw.CheckpointID, raw.CheckpointIDLegacy)
	r.FileCount = firstNonZero(raw.FileCount, raw.FileCountLegacy)
	r.Uploaded = firstNonZero(raw.Uploaded, raw.UploadedLegacy)
	r.Added = firstNonZero(raw.Added, raw.AddedLegacy)
	r.Deleted = firstNonZero(raw.Deleted, raw.DeletedLegacy)
	return nil
}

func firstNonEmpty(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func firstNonZero(primary, fallback int) int {
	if primary != 0 {
		return primary
	}
	return fallback
}

type statusError struct {
	path       string
	status     int
	body       string
	retryAfter time.Duration
}

func (e statusError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("openace daemon %s returned HTTP %d", e.path, e.status)
	}
	return fmt.Sprintf("openace daemon %s returned HTTP %d: %s", e.path, e.status, e.body)
}

func New(cfg Config) (*Client, error) {
	baseURL, err := normalizeBaseURL(cfg.DaemonAddr)
	if err != nil {
		return nil, err
	}
	maxOutputLength := cfg.MaxOutputLength
	if maxOutputLength <= 0 {
		maxOutputLength = DefaultMaxOutputLength
	}
	maxRetries := cfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}
	baseDelay := cfg.RetryBaseDelay
	if baseDelay < 0 {
		baseDelay = 0
	}
	maxDelay := cfg.RetryMaxDelay
	if maxDelay < 0 {
		maxDelay = 0
	}
	jitter := cfg.RetryJitter
	if jitter < 0 {
		jitter = 0
	}
	return &Client{
		baseURL:           baseURL,
		token:             strings.TrimSpace(cfg.DaemonToken),
		providerProfileID: strings.TrimSpace(cfg.ProviderProfileID),
		maxOutputLength:   maxOutputLength,
		maxRetries:        maxRetries,
		retryBaseDelay:    baseDelay,
		retryMaxDelay:     maxDelay,
		retryJitter:       jitter,
		http:              httpClient,
	}, nil
}

func (c *Client) Retrieve(ctx context.Context, req enhancer.Request) ([]string, error) {
	cwd := strings.TrimSpace(req.CWD)
	if cwd == "" {
		return nil, nil
	}
	query := BuildInformationRequest(req)
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	result, err := c.retrieve(ctx, retrieveRequest{
		DirectoryPath:      cwd,
		ProviderProfileID:  c.providerProfileID,
		InformationRequest: query,
		MaxOutputLength:    c.maxOutputLength,
	})
	if err != nil {
		return nil, fmt.Errorf("openace retrieval: %w", err)
	}
	formatted := formatRetrieval(result, cwd)
	if formatted == "" {
		return nil, nil
	}
	return []string{formatted}, nil
}

func (c *Client) retrieve(ctx context.Context, req retrieveRequest) (retrieveResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return retrieveResponse{}, err
	}
	var lastErr error
	attempts := c.maxRetries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		result, err := c.postOnce(ctx, "/v1/retrieve", payload)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isTransient(err) || attempt == attempts-1 {
			if isTransient(err) && attempt > 0 {
				return retrieveResponse{}, fmt.Errorf("failed after %d attempts: %w", attempt+1, err)
			}
			return retrieveResponse{}, err
		}
		if err := sleep(ctx, retryDelay(err, attempt, c.retryBaseDelay, c.retryMaxDelay, c.retryJitter)); err != nil {
			return retrieveResponse{}, err
		}
	}
	return retrieveResponse{}, lastErr
}

func (c *Client) postOnce(ctx context.Context, path string, payload []byte) (retrieveResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return retrieveResponse{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "application/json")
	httpReq.Header.Set("user-agent", "openpe-openace-context/0.1")
	if c.token != "" {
		httpReq.Header.Set("authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return retrieveResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return retrieveResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryAfter, _ := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now().UTC())
		return retrieveResponse{}, statusError{
			path:       path,
			status:     resp.StatusCode,
			body:       trimBody(data),
			retryAfter: retryAfter,
		}
	}
	var decoded retrieveResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return retrieveResponse{}, fmt.Errorf("decode openace retrieve response: %w", err)
	}
	return decoded, nil
}

func normalizeBaseURL(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = DefaultDaemonAddr
	}
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	parsed, err := url.ParseRequestURI(addr)
	if err != nil {
		return "", fmt.Errorf("invalid Openace daemon address: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || strings.TrimSpace(parsed.Hostname()) == "" {
		return "", fmt.Errorf("invalid Openace daemon address: expected http(s) host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid Openace daemon address: credentials, query, and fragment are not allowed")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func formatRetrieval(result retrieveResponse, cwd string) string {
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("Openace code retrieval context for workspace ")
	b.WriteString(strings.TrimSpace(cwd))
	b.WriteString(":\n")
	b.WriteString(text)
	summary := resultSummary(result)
	if summary != "" {
		b.WriteString("\n\n[openPE Openace summary: ")
		b.WriteString(summary)
		b.WriteString("]")
	}
	return b.String()
}

func resultSummary(result retrieveResponse) string {
	parts := make([]string, 0, 6)
	if result.ProviderProfileID != "" {
		parts = append(parts, "provider_profile_id="+result.ProviderProfileID)
	}
	if result.CheckpointID != "" {
		parts = append(parts, "checkpoint="+result.CheckpointID)
	}
	if result.FileCount > 0 {
		parts = append(parts, fmt.Sprintf("files=%d", result.FileCount))
	}
	if result.Uploaded > 0 {
		parts = append(parts, fmt.Sprintf("uploaded=%d", result.Uploaded))
	}
	if result.Added > 0 {
		parts = append(parts, fmt.Sprintf("added=%d", result.Added))
	}
	if result.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("deleted=%d", result.Deleted))
	}
	return strings.Join(parts, " ")
}

func isTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var status statusError
	if errors.As(err, &status) {
		return status.status == http.StatusRequestTimeout ||
			status.status == http.StatusTooManyRequests ||
			status.status == 499 ||
			status.status == http.StatusInternalServerError ||
			status.status == http.StatusBadGateway ||
			status.status == http.StatusServiceUnavailable ||
			status.status == http.StatusGatewayTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "temporary") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "connection reset") ||
		strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "server closed idle connection")
}

func retryDelay(err error, attempt int, baseDelay time.Duration, maxDelay time.Duration, jitter time.Duration) time.Duration {
	var status statusError
	if errors.As(err, &status) && status.retryAfter > 0 {
		return capDelay(addJitter(status.retryAfter, jitter), maxDelay)
	}
	if baseDelay <= 0 {
		return 0
	}
	delay := baseDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if maxDelay > 0 && delay >= maxDelay {
			delay = maxDelay
			break
		}
	}
	return capDelay(addJitter(delay, jitter), maxDelay)
}

func capDelay(delay time.Duration, maxDelay time.Duration) time.Duration {
	if maxDelay > 0 && delay > maxDelay {
		return maxDelay
	}
	return delay
}

func addJitter(delay time.Duration, jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return delay
	}
	return delay + time.Duration(time.Now().UnixNano()%int64(jitter))
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := when.Sub(now)
	if delay < 0 {
		return 0, true
	}
	return delay, true
}

func sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func trimBody(data []byte) string {
	body := strings.TrimSpace(string(data))
	runes := []rune(body)
	if len(runes) > 512 {
		return string(runes[:512]) + "..."
	}
	return body
}
