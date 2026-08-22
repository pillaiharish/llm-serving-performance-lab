package workload

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestDeterministicBuilderProducesExactStableTargets(t *testing.T) {
	for _, target := range []int{1, 2, 127, 128, 129, 512} {
		t.Run(strconv.Itoa(target), func(t *testing.T) {
			tokenizer := &byteTokenizer{maxLength: 4096}
			builder := NewDeterministicBuilder(tokenizer)
			spec := WorkloadSpec{TargetInputTokens: target, RequestedOutputTokens: 16, MaxInputTokens: 1024}
			first, err := builder.Build(context.Background(), spec)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			second, err := builder.Build(context.Background(), spec)
			if err != nil {
				t.Fatalf("second Build: %v", err)
			}
			if first.ResolvedInputTokens != target || len(first.Prompt) != target || first.Prompt != second.Prompt || first.PromptSHA256 != second.PromptSHA256 {
				t.Fatalf("unstable prepared workload: first=%+v second=%+v", first, second)
			}
			if first.Builder == nil || first.Builder.Version != BuilderVersion || first.Tokenizer == nil || first.InputContract != ContractRenderedChatInput {
				t.Fatalf("missing workload identity: %+v", first)
			}
			if tokenizer.calls > 20 {
				t.Fatalf("tokenizer calls = %d, want bounded construction", tokenizer.calls)
			}
		})
	}
}

func TestDeterministicBuilderRejectsInvalidImpossibleAndContextTargets(t *testing.T) {
	tests := []struct {
		name      string
		tokenizer *byteTokenizer
		spec      WorkloadSpec
		want      string
	}{
		{name: "zero", tokenizer: &byteTokenizer{maxLength: 100}, spec: WorkloadSpec{RequestedOutputTokens: 1, MaxInputTokens: 10}, want: "greater than zero"},
		{name: "over safety", tokenizer: &byteTokenizer{maxLength: 100}, spec: WorkloadSpec{TargetInputTokens: 11, RequestedOutputTokens: 1, MaxInputTokens: 10}, want: "safety"},
		{name: "below template overhead", tokenizer: &byteTokenizer{overhead: 8, maxLength: 100}, spec: WorkloadSpec{TargetInputTokens: 7, RequestedOutputTokens: 1, MaxInputTokens: 10}, want: "below chat-template minimum"},
		{name: "context", tokenizer: &byteTokenizer{maxLength: 10}, spec: WorkloadSpec{TargetInputTokens: 8, RequestedOutputTokens: 3, MaxInputTokens: 10}, want: "max_model_len"},
		{name: "impossible", tokenizer: &byteTokenizer{overhead: 2, multiplier: 2, maxLength: 100}, spec: WorkloadSpec{TargetInputTokens: 3, RequestedOutputTokens: 1, MaxInputTokens: 10}, want: "cannot construct exact"},
		{name: "tokenizer error", tokenizer: &byteTokenizer{maxLength: 100, err: errors.New("encode failed")}, spec: WorkloadSpec{TargetInputTokens: 3, RequestedOutputTokens: 1, MaxInputTokens: 10}, want: "encode failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewDeterministicBuilder(test.tokenizer).Build(context.Background(), test.spec)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPreparePromptRecordsOnlySafeMetadata(t *testing.T) {
	prepared := PreparePrompt("private prompt", 32)
	if prepared.Mode != ModePrompt || prepared.Prompt != "private prompt" || prepared.PromptBytes != len("private prompt") || len(prepared.PromptSHA256) != 64 || prepared.Tokenizer != nil || prepared.Builder != nil {
		t.Fatalf("prepared = %+v", prepared)
	}
}

type byteTokenizer struct {
	overhead   int
	multiplier int
	maxLength  int
	calls      int
	err        error
}

func (t *byteTokenizer) Encode(_ context.Context, text string) ([]int, TokenizationEvidence, error) {
	t.calls++
	if t.err != nil {
		return nil, TokenizationEvidence{}, t.err
	}
	multiplier := t.multiplier
	if multiplier == 0 {
		multiplier = 1
	}
	count := t.overhead + len([]byte(text))*multiplier
	return make([]int, count), TokenizationEvidence{Count: count, ModelMaxLength: t.maxLength}, nil
}

func (t *byteTokenizer) Count(ctx context.Context, text string) (int, TokenizationEvidence, error) {
	tokens, evidence, err := t.Encode(ctx, text)
	return len(tokens), evidence, err
}

func (t *byteTokenizer) Identity() TokenizerIdentity {
	return TokenizerIdentity{Adapter: vllmAdapter, AdapterVersion: vllmAdapterVersion, Contract: ContractRenderedChatInput, Model: "fixture", Source: "fixture", BehavioralFingerprintSHA256: strings.Repeat("a", 64), ModelMaxLength: t.maxLength}
}
