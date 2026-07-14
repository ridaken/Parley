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

// Client posts audio to a compatible transcription server's /inference endpoint.
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
	return c.postMultipart(ctx, "/inference", map[string]string{
		"response_format": "json",
		"temperature":     "0.0",
	}, wav, true)
}

// StreamFeed advances one persistent cache-aware ASR stream and returns any new
// text the model has emitted. streamID is normally the stable audio source label.
func (c *Client) StreamFeed(ctx context.Context, streamID string, wav []byte) (string, error) {
	return c.postMultipart(ctx, "/stream", map[string]string{
		"stream_id": streamID,
		"action":    "feed",
	}, wav, false)
}

// StreamFinish flushes and closes a persistent ASR stream.
func (c *Client) StreamFinish(ctx context.Context, streamID string) (string, error) {
	return c.postMultipart(ctx, "/stream", map[string]string{
		"stream_id": streamID,
		"action":    "finish",
	}, nil, false)
}

func (c *Client) postMultipart(ctx context.Context, endpoint string, fields map[string]string, wav []byte, trim bool) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	if wav != nil {
		fw, err := w.CreateFormFile("file", "chunk.wav")
		if err != nil {
			return "", err
		}
		if _, err := fw.Write(wav); err != nil {
			return "", err
		}
	}
	for name, value := range fields {
		if err := w.WriteField(name, value); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, &body)
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
		return "", fmt.Errorf("transcription request %s: status %d: %s", endpoint, resp.StatusCode, string(data))
	}

	var out inferenceResponse
	if err := json.Unmarshal(data, &out); err != nil {
		// Some builds return raw text; fall back to that.
		if trim {
			return strings.TrimSpace(string(data)), nil
		}
		return string(data), nil
	}
	if trim {
		return strings.TrimSpace(out.Text), nil
	}
	return out.Text, nil
}
