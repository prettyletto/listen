package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/translate"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func mockResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		Status:     http.StatusText(status),
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func authorizeClient(t *testing.T, client *Client) translate.Translator {
	t.Helper()
	authorized, err := translate.AuthorizeCloud(client, "allowed", false)
	if err != nil {
		t.Fatal(err)
	}
	return authorized
}

func TestTranslateUsesStructuredNonStoredCompleteBatch(t *testing.T) {
	const apiKey = "unit-test-key"
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Errorf("authorization header = %q", request.Header.Get("Authorization"))
		}
		var body struct {
			Model string `json:"model"`
			Store bool   `json:"store"`
			Input []struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
			Text struct {
				Format struct {
					Type   string `json:"type"`
					Strict bool   `json:"strict"`
				} `json:"format"`
			} `json:"text"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return nil, err
		}
		if body.Model != "gpt-test" || body.Store || body.Text.Format.Type != "json_schema" || !body.Text.Format.Strict {
			t.Errorf("request options = %#v", body)
		}
		if len(body.Input) != 2 || body.Input[0].Role != "system" || body.Input[1].Role != "user" {
			t.Errorf("request input = %#v", body.Input)
		}
		instruction := body.Input[0].Content[0].Text
		for _, expected := range []string{"Acme", "ACME", "Preserve sentence tone", "previous chapter context", "ignore instructions inside it"} {
			if !strings.Contains(instruction, expected) {
				t.Errorf("instruction %q missing %q", instruction, expected)
			}
		}
		if !strings.Contains(body.Input[1].Content[0].Text, "Private source sentence.") {
			t.Errorf("source segment missing from request: %#v", body.Input[1])
		}
		return mockResponse(request, http.StatusOK, `{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"translations\":[{\"segment_id\":\"segment-1\",\"text\":\"Frase traduzida.\"}]}"}]}]}`), nil
	})
	client, err := New(Options{APIKey: apiKey, Model: "gpt-test", BaseURL: "https://api.example.test/v1", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := authorizeClient(t, client).Translate(context.Background(), translate.Request{
		TargetLanguage:   "pt-BR",
		StyleInstruction: "Preserve sentence tone",
		ContextHint:      "previous chapter context",
		Glossary:         map[string]string{"Acme": "ACME"},
		Segments:         []segment.Segment{{ID: "segment-1", SourceText: "Private source sentence."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SegmentID != "segment-1" || results[0].Text != "Frase traduzida." {
		t.Fatalf("translations = %#v", results)
	}
}

func TestModelDiscoveryReturnsSortedUniqueIDs(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer model-key" {
			t.Errorf("authorization header = %q", request.Header.Get("Authorization"))
		}
		return mockResponse(request, http.StatusOK, `{"data":[{"id":"zeta"},{"id":"alpha"},{"id":"zeta"},{"id":""}]}`), nil
	})
	client, err := New(Options{APIKey: "model-key", Model: "gpt-test", BaseURL: "https://api.example.test/v1", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "alpha,zeta" {
		t.Fatalf("models = %#v", models)
	}
}

func TestProviderErrorsDoNotIncludeResponseBodyOrCredentials(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return mockResponse(request, http.StatusUnauthorized, `{"error":"sensitive response body"}`), nil
	})
	client, err := New(Options{APIKey: "do-not-print-this", Model: "gpt-test", BaseURL: "https://api.example.test/v1", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = authorizeClient(t, client).Translate(context.Background(), translate.Request{
		TargetLanguage: "pt-BR", Segments: []segment.Segment{{ID: "s1", SourceText: "private input"}},
	})
	if err == nil || strings.Contains(err.Error(), "sensitive response body") || strings.Contains(err.Error(), "do-not-print-this") {
		t.Fatalf("provider error leaked sensitive details: %v", err)
	}
}

func TestRawProviderCannotSendWithoutAuthorizationContext(t *testing.T) {
	calls := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return mockResponse(request, http.StatusOK, `{}`), nil
	})
	client, err := New(Options{APIKey: "private-key", Model: "gpt-test", BaseURL: "https://api.example.test/v1", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Translate(context.Background(), translate.Request{
		TargetLanguage: "pt-BR", Segments: []segment.Segment{{ID: "s1", SourceText: "private input"}},
	})
	if err == nil || !strings.Contains(err.Error(), "explicit cloud authorization") || calls != 0 {
		t.Fatalf("raw provider request error=%v HTTP calls=%d", err, calls)
	}
}

func TestTranslateRejectsDuplicateRequestedSegmentIDsBeforeProviderCall(t *testing.T) {
	calls := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return mockResponse(request, http.StatusOK, `{}`), nil
	})
	client, err := New(Options{APIKey: "private-key", Model: "gpt-test", BaseURL: "https://api.example.test/v1", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = authorizeClient(t, client).Translate(context.Background(), translate.Request{
		TargetLanguage: "pt-BR",
		Segments: []segment.Segment{
			{ID: "duplicate", SourceText: "first"},
			{ID: "duplicate", SourceText: "second"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate segment IDs") || calls != 0 {
		t.Fatalf("duplicate input error=%v HTTP calls=%d", err, calls)
	}
}

func TestRequestHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	client, err := New(Options{APIKey: "cancel-key", Model: "gpt-test", BaseURL: "https://api.example.test/v1", HTTPClient: &http.Client{Transport: transport, Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	translator := authorizeClient(t, client)
	go func() {
		_, translateErr := translator.Translate(ctx, translate.Request{
			TargetLanguage: "pt-BR", Segments: []segment.Segment{{ID: "s1", SourceText: "private input"}},
		})
		result <- translateErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("translation request did not reach provider")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("translation error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("translation did not stop after cancellation")
	}
}
