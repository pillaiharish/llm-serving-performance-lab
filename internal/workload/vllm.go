package workload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	vllmAdapter        = "vllm_chat_render"
	vllmAdapterVersion = "1"
	maxTokenizerBody   = 8 << 20
)

var fingerprintProbes = []string{
	"",
	"Slentore tokenizer probe.",
	"One two 3 — 四.",
}

type VLLMTokenizer struct {
	httpClient  *http.Client
	url         string
	apiKey      string
	model       string
	identity    TokenizerIdentity
	maxLength   int
	initialized bool
}

func NewVLLMTokenizer(httpClient *http.Client, tokenizerURL, apiKey, model string) (*VLLMTokenizer, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("HTTP client is required")
	}
	if strings.TrimSpace(tokenizerURL) == "" {
		return nil, fmt.Errorf("tokenizer URL is required")
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("tokenizer model is required")
	}
	return &VLLMTokenizer{
		httpClient: httpClient,
		url:        tokenizerURL,
		apiKey:     apiKey,
		model:      model,
		identity: TokenizerIdentity{
			Adapter:        vllmAdapter,
			AdapterVersion: vllmAdapterVersion,
			Contract:       ContractRenderedChatInput,
			Model:          model,
			Source:         tokenizerURL,
		},
	}, nil
}

func (t *VLLMTokenizer) Initialize(ctx context.Context) error {
	if t == nil {
		return fmt.Errorf("tokenizer is required")
	}
	type fingerprint struct {
		Adapter        string  `json:"adapter"`
		AdapterVersion string  `json:"adapter_version"`
		Contract       string  `json:"contract"`
		Model          string  `json:"model"`
		Source         string  `json:"source"`
		Probes         [][]int `json:"probes"`
	}
	value := fingerprint{
		Adapter: t.identity.Adapter, AdapterVersion: t.identity.AdapterVersion,
		Contract: t.identity.Contract, Model: t.model, Source: t.url,
	}
	for _, probe := range fingerprintProbes {
		tokens, evidence, err := t.encode(ctx, probe)
		if err != nil {
			return fmt.Errorf("tokenizer identity probe: %w", err)
		}
		if err := t.observeMaxLength(evidence.ModelMaxLength); err != nil {
			return err
		}
		value.Probes = append(value.Probes, tokens)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode tokenizer fingerprint evidence: %w", err)
	}
	digest := sha256.Sum256(encoded)
	t.identity.BehavioralFingerprintSHA256 = hex.EncodeToString(digest[:])
	t.identity.ModelMaxLength = t.maxLength
	t.initialized = true
	return nil
}

func (t *VLLMTokenizer) Identity() TokenizerIdentity {
	if t == nil {
		return TokenizerIdentity{}
	}
	return t.identity
}

func (t *VLLMTokenizer) Encode(ctx context.Context, text string) ([]int, TokenizationEvidence, error) {
	if t == nil || !t.initialized {
		return nil, TokenizationEvidence{}, fmt.Errorf("tokenizer is not initialized")
	}
	tokens, evidence, err := t.encode(ctx, text)
	if err != nil {
		return nil, TokenizationEvidence{}, err
	}
	if err := t.observeMaxLength(evidence.ModelMaxLength); err != nil {
		return nil, TokenizationEvidence{}, err
	}
	return tokens, evidence, nil
}

func (t *VLLMTokenizer) Count(ctx context.Context, text string) (int, TokenizationEvidence, error) {
	tokens, evidence, err := t.Encode(ctx, text)
	if err != nil {
		return 0, TokenizationEvidence{}, err
	}
	return len(tokens), evidence, nil
}

func (t *VLLMTokenizer) observeMaxLength(value int) error {
	if value <= 0 {
		return fmt.Errorf("tokenizer response max_model_len must be greater than zero")
	}
	if t.maxLength != 0 && t.maxLength != value {
		return fmt.Errorf("tokenizer response max_model_len changed from %d to %d", t.maxLength, value)
	}
	t.maxLength = value
	return nil
}

func (t *VLLMTokenizer) encode(ctx context.Context, text string) ([]int, TokenizationEvidence, error) {
	payload, err := json.Marshal(vllmTokenizeRequest{
		Model:               t.model,
		Messages:            []vllmMessage{{Role: "user", Content: text}},
		AddGenerationPrompt: true,
		AddSpecialTokens:    false,
		ReturnTokenStrings:  false,
	})
	if err != nil {
		return nil, TokenizationEvidence{}, fmt.Errorf("encode tokenizer request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(payload))
	if err != nil {
		return nil, TokenizationEvidence{}, fmt.Errorf("create tokenizer request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if t.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+t.apiKey)
	}
	response, err := t.httpClient.Do(request)
	if err != nil {
		return nil, TokenizationEvidence{}, fmt.Errorf("send tokenizer request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxTokenizerBody))
		return nil, TokenizationEvidence{}, fmt.Errorf("tokenizer endpoint returned HTTP status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxTokenizerBody))
	var decoded vllmTokenizeResponse
	if err := decoder.Decode(&decoded); err != nil {
		return nil, TokenizationEvidence{}, fmt.Errorf("decode tokenizer response: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, TokenizationEvidence{}, fmt.Errorf("tokenizer response must contain exactly one JSON value")
	}
	if decoded.Count < 0 || decoded.Count != len(decoded.Tokens) {
		return nil, TokenizationEvidence{}, fmt.Errorf("tokenizer response count does not match token IDs")
	}
	for _, token := range decoded.Tokens {
		if token < 0 {
			return nil, TokenizationEvidence{}, fmt.Errorf("tokenizer response contains a negative token ID")
		}
	}
	return append([]int(nil), decoded.Tokens...), TokenizationEvidence{Count: decoded.Count, ModelMaxLength: decoded.MaxModelLength}, nil
}

type vllmTokenizeRequest struct {
	Model               string        `json:"model"`
	Messages            []vllmMessage `json:"messages"`
	AddGenerationPrompt bool          `json:"add_generation_prompt"`
	AddSpecialTokens    bool          `json:"add_special_tokens"`
	ReturnTokenStrings  bool          `json:"return_token_strs"`
}

type vllmMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type vllmTokenizeResponse struct {
	Count          int   `json:"count"`
	MaxModelLength int   `json:"max_model_len"`
	Tokens         []int `json:"tokens"`
}
