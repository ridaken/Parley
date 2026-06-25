// Package llm talks to any OpenAI-compatible chat-completions endpoint (local
// llama-server / LM Studio / Ollama, or a cloud provider).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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

const defaultMaxTokens = 2048

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
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens"`
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
	if err != nil && responseFormatUnsupported(err) {
		return c.complete(ctx, messages, false)
	}
	return reply, err
}

func (c *Client) complete(ctx context.Context, messages []Message, jsonMode bool) (string, error) {
	reqBody := chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.2,
		MaxTokens:   defaultMaxTokens,
		Stream:      false,
	}
	if jsonMode {
		reqBody.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: status %d: %s", resp.StatusCode, string(data))
	}

	var out chatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("llm: decode response: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("llm: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: empty response")
	}
	if out.Usage.PromptTokens != 0 || out.Usage.CompletionTokens != 0 || out.Usage.TotalTokens != 0 {
		log.Printf("[llm] tokens prompt=%d completion=%d total=%d",
			out.Usage.PromptTokens, out.Usage.CompletionTokens, out.Usage.TotalTokens)
	}
	reply := out.Choices[0].replyText()
	if strings.TrimSpace(reply) == "" {
		if out.Choices[0].FinishReason != "" {
			return "", fmt.Errorf("llm: empty assistant content (finish_reason=%s)", out.Choices[0].FinishReason)
		}
		return "", fmt.Errorf("llm: empty assistant content")
	}
	return reply, nil
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
