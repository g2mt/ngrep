package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxDimensions = 65536

type apiClient struct {
	endpoint, model, key string
	http                 *http.Client
}

type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("embedding API (HTTP %d): %s", e.status, e.message)
}

func (e *apiError) tooLong() bool {
	if e.status != http.StatusBadRequest && e.status != http.StatusRequestEntityTooLarge {
		return false
	}
	message := strings.ToLower(e.code + " " + e.message)
	return e.status == http.StatusRequestEntityTooLarge || strings.Contains(message, "context_length_exceeded") ||
		strings.Contains(message, "maximum context length") || strings.Contains(message, "input is too long") ||
		strings.Contains(message, "too many tokens") || strings.Contains(message, "maximum number of tokens")
}

func validateKey(key string) error {
	if key == "" || strings.ContainsAny(key, "\r\n\x00") {
		return errors.New("API key must be nonempty and cannot contain newlines or NUL bytes")
	}
	return nil
}

func newAPIClient(s embeddingSettings, key string) (*apiClient, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	u, err := url.Parse(s.APIHost)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("embedding.api_host must be an HTTP(S) base URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(s.Model) == "" {
		return nil, errors.New("embedding.model must not be empty")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/embeddings"
	u.RawPath = ""
	return &apiClient{endpoint: u.String(), model: s.Model, key: key, http: &http.Client{
		Timeout: 90 * time.Second,
		// Do not redirect private file contents or Authorization headers.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *apiClient) embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(struct {
		Model          string `json:"model"`
		Input          string `json:"input"`
		EncodingFormat string `json:"encoding_format"`
	}{c.model, strings.ToValidUTF8(text, "\ufffd"), "float"})
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		vector, err := c.request(ctx, body)
		var apiErr *apiError
		if !errors.As(err, &apiErr) || attempt >= 2 || (apiErr.status != 429 && apiErr.status < 500) {
			return vector, err
		}
		timer := time.NewTimer(time.Second << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *apiClient) request(ctx context.Context, body []byte) ([]float32, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 32<<20 {
		return nil, errors.New("embedding API response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		var reply struct {
			Error struct {
				Message string          `json:"message"`
				Code    json.RawMessage `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &reply)
		message := reply.Error.Message
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		message = strings.ReplaceAll(message, c.key, "[redacted]")
		if len(message) > 2000 {
			message = message[:2000]
		}
		return nil, &apiError{status: resp.StatusCode, code: string(reply.Error.Code), message: message}
	}
	var reply struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, fmt.Errorf("invalid embedding response: %w", err)
	}
	if len(reply.Data) != 1 || reply.Data[0].Index != 0 || !validVector(reply.Data[0].Embedding) {
		return nil, errors.New("embedding API returned an invalid, empty, or nonfinite vector")
	}
	return reply.Data[0].Embedding, nil
}

func validVector(v []float32) bool {
	if len(v) < 1 || len(v) > maxDimensions {
		return false
	}
	var norm float64
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
		norm += f * f
	}
	return norm > 0
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return math.Inf(-1)
	}
	var dot, aa, bb float64
	for i, x := range a {
		y := float64(b[i])
		fx := float64(x)
		dot += fx * y
		aa += fx * fx
		bb += y * y
	}
	return dot / (math.Sqrt(aa) * math.Sqrt(bb))
}
