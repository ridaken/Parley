// Package llm talks to any OpenAI-compatible chat-completions endpoint (local
// llama-server / LM Studio / Ollama, or a cloud provider).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Message is a single chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client issues chat-completion requests.
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewClient builds a client. baseURL should include the API root (e.g.
// http://127.0.0.1:8080/v1). apiKey may be empty for local servers.
func NewClient(baseURL, apiKey, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 90 * time.Second},
	}
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Stream         bool            `json:"stream"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// RequestDiagnostics captures the transport details that are useful when an
// OpenAI-compatible endpoint fails before producing a usable completion.
type RequestDiagnostics struct {
	Method           string `json:"method,omitempty"`
	URL              string `json:"url,omitempty"`
	Model            string `json:"model,omitempty"`
	JSONMode         bool   `json:"jsonMode"`
	ResponseFormat   string `json:"responseFormat,omitempty"`
	StatusCode       int    `json:"statusCode,omitempty"`
	Status           string `json:"status,omitempty"`
	ResponseBody     string `json:"responseBody,omitempty"`
	ErrorType        string `json:"errorType,omitempty"`
	Error            string `json:"error,omitempty"`
	ElapsedMs        int64  `json:"elapsedMs,omitempty"`
	FinishReason     string `json:"finishReason,omitempty"`
	PromptTokens     int    `json:"promptTokens,omitempty"`
	CompletionTokens int    `json:"completionTokens,omitempty"`
	TotalTokens      int    `json:"totalTokens,omitempty"`
}

// RequestError keeps HTTP status, body, and transport exception details intact
// so callers can write useful diagnostics instead of a flattened error string.
type RequestError struct {
	diagnostics RequestDiagnostics
	cause       error
}

func (e *RequestError) Error() string {
	if e == nil {
		return ""
	}
	if e.diagnostics.StatusCode != 0 && e.diagnostics.StatusCode != http.StatusOK {
		return fmt.Sprintf("llm: status %d: %s", e.diagnostics.StatusCode, e.diagnostics.ResponseBody)
	}
	if e.cause != nil {
		return "llm: " + e.cause.Error()
	}
	return e.diagnostics.Error
}

func (e *RequestError) Unwrap() error { return e.cause }

func (e *RequestError) Diagnostics() RequestDiagnostics {
	if e == nil {
		return RequestDiagnostics{}
	}
	return e.diagnostics
}

// JSONFallbackError reports that the JSON-mode attempt failed, then the
// compatibility retry without response_format failed too.
type JSONFallbackError struct {
	JSONModeErr  error
	FallbackErr  error
	Diagnostics  []RequestDiagnostics
	FallbackUsed bool
}

func (e *JSONFallbackError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("llm json-mode request failed (%v); fallback without response_format failed (%v)", e.JSONModeErr, e.FallbackErr)
}

func (e *JSONFallbackError) Unwrap() error { return e.FallbackErr }

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   Usage        `json:"usage"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type chatChoice struct {
	Message          responseMessage `json:"message"`
	Text             string          `json:"text"`
	ReasoningContent string          `json:"reasoning_content"`
	FinishReason     string          `json:"finish_reason"`
}

type responseMessage struct {
	Role             string `json:"role"`
	Content          any    `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
}

// Complete sends the messages and returns the assistant's reply text.
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	return c.complete(ctx, messages, false)
}

// CompleteJSON asks compatible endpoints to constrain output to a JSON object.
// Some local OpenAI-compatible servers reject response_format; in that case we
// retry once without it so live analysis still works with those providers.
func (c *Client) CompleteJSON(ctx context.Context, messages []Message) (string, error) {
	reply, err := c.complete(ctx, messages, true)
	if err != nil && shouldRetryWithoutResponseFormat(err) {
		fallbackReply, fallbackErr := c.complete(ctx, messages, false)
		if fallbackErr != nil {
			return "", &JSONFallbackError{
				JSONModeErr:  err,
				FallbackErr:  fallbackErr,
				Diagnostics:  Diagnostics(err, fallbackErr),
				FallbackUsed: true,
			}
		}
		return fallbackReply, nil
	}
	return reply, err
}

func (c *Client) complete(ctx context.Context, messages []Message, jsonMode bool) (string, error) {
	start := time.Now()
	url := c.baseURL + "/chat/completions"
	reqBody := chatRequest{
		Model:    c.model,
		Messages: messages,
		Stream:   false,
	}
	if jsonMode {
		reqBody.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", c.requestError(req, jsonMode, start, 0, "", "", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", c.requestError(req, jsonMode, start, resp.StatusCode, resp.Status, "", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", c.requestError(req, jsonMode, start, resp.StatusCode, resp.Status, string(data), nil)
	}

	var out chatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", c.requestError(req, jsonMode, start, resp.StatusCode, resp.Status, string(data), fmt.Errorf("decode response: %w", err))
	}
	if out.Error != nil {
		return "", c.requestError(req, jsonMode, start, resp.StatusCode, resp.Status, string(data), errors.New(out.Error.Message))
	}
	if len(out.Choices) == 0 {
		return "", c.requestError(req, jsonMode, start, resp.StatusCode, resp.Status, string(data), fmt.Errorf("empty response"))
	}
	choice := out.Choices[0]
	if choice.FinishReason != "" || out.Usage.PromptTokens != 0 || out.Usage.CompletionTokens != 0 || out.Usage.TotalTokens != 0 {
		log.Printf("[llm] finish_reason=%q tokens prompt=%d completion=%d total=%d",
			choice.FinishReason, out.Usage.PromptTokens, out.Usage.CompletionTokens, out.Usage.TotalTokens)
	}
	reply := choice.replyText()
	if strings.EqualFold(choice.FinishReason, "length") {
		return "", c.completionError(req, jsonMode, start, resp.StatusCode, resp.Status, string(data), choice, out.Usage,
			fmt.Errorf("completion truncated (finish_reason=%s)", choice.FinishReason))
	}
	if strings.TrimSpace(reply) == "" {
		if choice.FinishReason != "" {
			return "", c.completionError(req, jsonMode, start, resp.StatusCode, resp.Status, string(data), choice, out.Usage,
				fmt.Errorf("empty assistant content (finish_reason=%s)", choice.FinishReason))
		}
		return "", c.requestError(req, jsonMode, start, resp.StatusCode, resp.Status, string(data), fmt.Errorf("empty assistant content"))
	}
	return reply, nil
}

func (c *Client) completionError(req *http.Request, jsonMode bool, start time.Time, statusCode int, status, body string, choice chatChoice, usage Usage, cause error) error {
	err := c.requestError(req, jsonMode, start, statusCode, status, body, cause)
	var reqErr *RequestError
	if errors.As(err, &reqErr) {
		reqErr.diagnostics.FinishReason = choice.FinishReason
		reqErr.diagnostics.PromptTokens = usage.PromptTokens
		reqErr.diagnostics.CompletionTokens = usage.CompletionTokens
		reqErr.diagnostics.TotalTokens = usage.TotalTokens
	}
	return err
}

func (c *Client) requestError(req *http.Request, jsonMode bool, start time.Time, statusCode int, status, body string, cause error) error {
	diag := RequestDiagnostics{
		Method:         req.Method,
		URL:            req.URL.String(),
		Model:          c.model,
		JSONMode:       jsonMode,
		StatusCode:     statusCode,
		Status:         status,
		ResponseBody:   truncateForLog(strings.TrimSpace(body), 4000),
		ElapsedMs:      time.Since(start).Milliseconds(),
		ResponseFormat: "",
	}
	if jsonMode {
		diag.ResponseFormat = "json_object"
	}
	if cause != nil {
		diag.ErrorType = errorType(cause)
		diag.Error = cause.Error()
	}
	return &RequestError{diagnostics: diag, cause: cause}
}

// Ping does a tiny request to verify the endpoint/model/key are usable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Complete(ctx, []Message{{Role: "user", Content: "ping"}})
	return err
}

func (ch chatChoice) replyText() string {
	for _, text := range []string{
		textContent(ch.Message.Content),
		ch.Text,
		ch.Message.ReasoningContent,
		ch.ReasoningContent,
	} {
		if strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func textContent(v any) string {
	switch c := v.(type) {
	case nil:
		return ""
	case string:
		return c
	case []any:
		parts := make([]string, 0, len(c))
		for _, part := range c {
			if text := textContent(part); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "")
	case map[string]any:
		for _, key := range []string{"text", "content"} {
			if text := textContent(c[key]); strings.TrimSpace(text) != "" {
				return text
			}
		}
		return ""
	default:
		return fmt.Sprint(c)
	}
}

func responseFormatUnsupported(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "response_format") ||
		strings.Contains(msg, "json_object") ||
		strings.Contains(msg, "unsupported")
}

func shouldRetryWithoutResponseFormat(err error) bool {
	return responseFormatUnsupported(err) || isLikelyResponseFormatTransportDrop(err)
}

func isLikelyResponseFormatTransportDrop(err error) bool {
	var reqErr *RequestError
	if !errors.As(err, &reqErr) {
		return false
	}
	diag := reqErr.Diagnostics()
	if !diag.JSONMode || diag.Error == "" {
		return false
	}
	if diag.StatusCode != 0 && diag.StatusCode != http.StatusOK {
		return false
	}
	msg := strings.ToLower(diag.Error)
	return strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "server closed idle connection") ||
		strings.Contains(msg, "unexpected eof")
}

// Diagnostics extracts every LLM request attempt embedded in err. The optional
// additional errors are included in order, which is useful for JSON fallback.
func Diagnostics(err error, additional ...error) []RequestDiagnostics {
	var out []RequestDiagnostics
	collectDiagnostics(err, &out)
	for _, e := range additional {
		collectDiagnostics(e, &out)
	}
	return out
}

func collectDiagnostics(err error, out *[]RequestDiagnostics) {
	if err == nil {
		return
	}
	var fallback *JSONFallbackError
	if errors.As(err, &fallback) && len(fallback.Diagnostics) > 0 {
		*out = append(*out, fallback.Diagnostics...)
		return
	}
	var reqErr *RequestError
	if errors.As(err, &reqErr) {
		*out = append(*out, reqErr.Diagnostics())
	}
}

func errorType(err error) string {
	if err == nil {
		return ""
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return fmt.Sprintf("%T", urlErr.Err)
	}
	return fmt.Sprintf("%T", err)
}

func truncateForLog(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
