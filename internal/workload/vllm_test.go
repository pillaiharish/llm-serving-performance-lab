package workload

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVLLMTokenizerUsesRenderedChatContractAndStableFingerprint(t *testing.T) {
	var requests []vllmTokenizeRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer private-key" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %v", request.Header)
		}
		var payload vllmTokenizeRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("Decode: %v", err)
			return
		}
		requests = append(requests, payload)
		count := 8 + len([]byte(payload.Messages[0].Content))
		tokens := make([]int, count)
		for index := range tokens {
			tokens[index] = index + 1
		}
		_ = json.NewEncoder(writer).Encode(vllmTokenizeResponse{Count: count, MaxModelLength: 4096, Tokens: tokens})
	}))
	defer server.Close()

	first, err := NewVLLMTokenizer(server.Client(), server.URL, "private-key", "Qwen/fixture")
	if err != nil {
		t.Fatalf("NewVLLMTokenizer: %v", err)
	}
	if err := first.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	count, evidence, err := first.Count(context.Background(), "abc")
	if err != nil || count != 11 || evidence.ModelMaxLength != 4096 {
		t.Fatalf("Count = %d, evidence=%+v, err=%v", count, evidence, err)
	}
	identity := first.Identity()
	if identity.Contract != ContractRenderedChatInput || identity.ModelMaxLength != 4096 || len(identity.BehavioralFingerprintSHA256) != 64 || identity.Revision != nil || identity.VocabularySHA256 != nil {
		t.Fatalf("identity = %+v", identity)
	}
	for _, payload := range requests {
		if payload.Model != "Qwen/fixture" || len(payload.Messages) != 1 || payload.Messages[0].Role != "user" || !payload.AddGenerationPrompt || payload.AddSpecialTokens || payload.ReturnTokenStrings {
			t.Fatalf("payload = %+v", payload)
		}
	}

	second, _ := NewVLLMTokenizer(server.Client(), server.URL, "private-key", "Qwen/fixture")
	if err := second.Initialize(context.Background()); err != nil {
		t.Fatalf("second Initialize: %v", err)
	}
	if first.Identity().BehavioralFingerprintSHA256 != second.Identity().BehavioralFingerprintSHA256 {
		t.Fatal("behavioral fingerprint is not deterministic")
	}
}

func TestVLLMTokenizerRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   any
		want   string
	}{
		{name: "status", status: http.StatusBadRequest, body: map[string]string{"error": "private prompt must not leak"}, want: "HTTP status 400"},
		{name: "count mismatch", status: http.StatusOK, body: vllmTokenizeResponse{Count: 2, MaxModelLength: 10, Tokens: []int{1}}, want: "count does not match"},
		{name: "negative token", status: http.StatusOK, body: vllmTokenizeResponse{Count: 1, MaxModelLength: 10, Tokens: []int{-1}}, want: "negative token"},
		{name: "zero max", status: http.StatusOK, body: vllmTokenizeResponse{Count: 0, MaxModelLength: 0, Tokens: []int{}}, want: "max_model_len"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_ = json.NewEncoder(writer).Encode(test.body)
			}))
			defer server.Close()
			tokenizer, _ := NewVLLMTokenizer(server.Client(), server.URL, "", "model")
			err := tokenizer.Initialize(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "private prompt") {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVLLMTokenizerHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	tokenizer, _ := NewVLLMTokenizer(server.Client(), server.URL, "", "model")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := tokenizer.Initialize(ctx)
	if err == nil {
		t.Fatal("Initialize unexpectedly succeeded")
	}
}

func TestVLLMFingerprintProbeSetIsFixed(t *testing.T) {
	want := []string{"", "Slentore tokenizer probe.", "One two 3 — 四."}
	if !reflect.DeepEqual(fingerprintProbes, want) {
		t.Fatalf("fingerprint probes = %q", fingerprintProbes)
	}
}
