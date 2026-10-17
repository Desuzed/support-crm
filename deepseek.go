package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ModelMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Generator interface {
	Generate(context.Context, []ModelMessage) (string, error)
}
type DeepSeek struct {
	Key, Model, Endpoint string
	HTTP                 *http.Client
}

func NewDeepSeek(key, model string) *DeepSeek {
	return &DeepSeek{Key: key, Model: model, Endpoint: "https://api.deepseek.com/chat/completions", HTTP: &http.Client{Timeout: 65 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (d *DeepSeek) Generate(ctx context.Context, messages []ModelMessage) (string, error) {
	if strings.TrimSpace(d.Key) == "" {
		return "", errors.New("deepseek_key_missing")
	}
	body, err := json.Marshal(map[string]any{"model": d.Model, "messages": messages, "thinking": map[string]string{"type": "disabled"}, "temperature": 0.2, "max_tokens": 2800, "response_format": map[string]string{"type": "json_object"}, "stream": false})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("deepseek_request_failed")
	}
	req.Header.Set("Authorization", "Bearer "+d.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", errors.New("deepseek_timeout")
		}
		return "", errors.New("deepseek_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("deepseek_http_%d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message      ModelMessage `json:"message"`
			FinishReason string       `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&out); err != nil {
		return "", errors.New("deepseek_invalid_response")
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", errors.New("deepseek_empty_response")
	}
	if out.Choices[0].FinishReason == "length" {
		return "", errors.New("deepseek_output_truncated")
	}
	return out.Choices[0].Message.Content, nil
}
