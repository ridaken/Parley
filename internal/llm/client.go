// Package llm talks to any OpenAI-compatible chat-completions endpoint (local
// llama-server / LM Studio / Ollama, or a cloud provider).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	Stream      bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends the messages and returns the assistant's reply text.
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	payload, err := json.Marshal(chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.2,
		Stream:      false,
	})
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
	return out.Choices[0].Message.Content, nil
}

// Ping does a tiny request to verify the endpoint/model/key are usable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Complete(ctx, []Message{{Role: "user", Content: "ping"}})
	return err
}
