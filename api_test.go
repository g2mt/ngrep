package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAPIRequest(t *testing.T) {
	c, err := newAPIClient(embeddingSettings{APIHost: "https://example.test/v1/", Model: "test"}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://example.test/v1/embeddings" || req.Method != "POST" {
			t.Fatalf("wrong request: %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("missing authorization")
		}
		var body map[string]string
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "test" || body["input"] != "hello\ufffd" || body["encoding_format"] != "float" {
			t.Fatalf("wrong body: %#v", body)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"index":0,"embedding":[1,2]}]}`)), Header: make(http.Header)}, nil
	})
	got, err := c.embed(context.Background(), "hello\xff")
	if err != nil || len(got) != 2 || got[1] != 2 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAPIErrors(t *testing.T) {
	for _, status := range []int{400, 401, 403} {
		c, err := newAPIClient(defaultSettings().Embedding, "secret")
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"secret: maximum context length exceeded","code":"context_length_exceeded"}}`)), Header: make(http.Header)}, nil
		})
		_, err = c.embed(context.Background(), "text")
		if err == nil || calls != 1 {
			t.Fatalf("status %d: %v, calls %d", status, err, calls)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("key leaked")
		}
		if isTooLong(err) != (status == 400) {
			t.Fatalf("incorrect token limit detection for %d", status)
		}
	}
}

func TestVectorValidationAndCosine(t *testing.T) {
	for _, v := range [][]float32{nil, {0, 0}, {float32(math.NaN())}, {float32(math.Inf(1))}} {
		if validVector(v) {
			t.Fatalf("accepted %v", v)
		}
	}
	if cosine([]float32{1, 0}, []float32{0, 1}) != 0 {
		t.Fatal("orthogonal cosine")
	}
	if got := cosine([]float32{3, 4}, []float32{6, 8}); math.Abs(got-1) > 1e-10 {
		t.Fatalf("cosine = %v", got)
	}
}

func TestAPIRejectsUnsafeHostsAndRedirects(t *testing.T) {
	for _, host := range []string{"file:///tmp/secret", "https://name:secret@example.test", "https://example.test?key=secret", ""} {
		if _, err := newAPIClient(embeddingSettings{APIHost: host, Model: "m"}, "key"); err == nil {
			t.Fatalf("accepted %q", host)
		}
	}
	c, err := newAPIClient(defaultSettings().Embedding, "key")
	if err != nil {
		t.Fatal(err)
	}
	if c.http.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("private requests may be redirected")
	}
}
