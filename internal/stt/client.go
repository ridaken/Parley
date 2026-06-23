package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// Client posts audio to a whisper.cpp server's /inference endpoint.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient builds a transcription client for the given server base URL.
func NewClient(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: 120 * time.Second}}
}

type inferenceResponse struct {
	Text string `json:"text"`
}

// Transcribe sends a WAV payload and returns the recognised text (trimmed).
func (c *Client) Transcribe(ctx context.Context, wav []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	fw, err := w.CreateFormFile("file", "chunk.wav")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(wav); err != nil {
		return "", err
	}
	_ = w.WriteField("response_format", "json")
	_ = w.WriteField("temperature", "0.0")
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/inference", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

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
		return "", fmt.Errorf("whisper inference: status %d: %s", resp.StatusCode, string(data))
	}

	var out inferenceResponse
	if err := json.Unmarshal(data, &out); err != nil {
		// Some builds return raw text; fall back to that.
		return strings.TrimSpace(string(data)), nil
	}
	return strings.TrimSpace(out.Text), nil
}
