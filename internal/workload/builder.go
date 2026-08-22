package workload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

var deterministicAtoms = []string{" x", " a", ".", "z", " 0"}

var refinementFragments = []string{
	" x", " a", " the", ".", ",", "!", "?", "0", "1", "A", "z", "\n",
	" benchmark", " fixture", " deterministic", " slentore",
}

type DeterministicBuilder struct {
	tokenizer Tokenizer
}

func NewDeterministicBuilder(tokenizer Tokenizer) *DeterministicBuilder {
	return &DeterministicBuilder{tokenizer: tokenizer}
}

func PreparePrompt(prompt string, requestedOutputTokens int) PreparedWorkload {
	digest := sha256.Sum256([]byte(prompt))
	return PreparedWorkload{
		Prompt: prompt, Mode: ModePrompt, RequestedOutputTokens: requestedOutputTokens,
		PromptBytes: len([]byte(prompt)), PromptSHA256: hex.EncodeToString(digest[:]),
	}
}

func (b *DeterministicBuilder) Build(ctx context.Context, spec WorkloadSpec) (PreparedWorkload, error) {
	if ctx == nil {
		return PreparedWorkload{}, fmt.Errorf("workload context is required")
	}
	if b == nil || b.tokenizer == nil {
		return PreparedWorkload{}, fmt.Errorf("workload tokenizer is required")
	}
	if spec.TargetInputTokens <= 0 {
		return PreparedWorkload{}, fmt.Errorf("target input tokens must be greater than zero")
	}
	if spec.RequestedOutputTokens <= 0 {
		return PreparedWorkload{}, fmt.Errorf("requested output tokens must be greater than zero")
	}
	if spec.MaxInputTokens <= 0 || spec.TargetInputTokens > spec.MaxInputTokens {
		return PreparedWorkload{}, fmt.Errorf("target input tokens exceed the workload safety limit")
	}

	minimum, evidence, err := b.tokenizer.Count(ctx, "")
	if err != nil {
		return PreparedWorkload{}, fmt.Errorf("count empty rendered chat input: %w", err)
	}
	if spec.TargetInputTokens < minimum {
		return PreparedWorkload{}, fmt.Errorf("target rendered input %d is below chat-template minimum %d", spec.TargetInputTokens, minimum)
	}
	if spec.TargetInputTokens > int(^uint(0)>>1)-spec.RequestedOutputTokens {
		return PreparedWorkload{}, fmt.Errorf("workload token total overflows int")
	}
	if evidence.ModelMaxLength > 0 && spec.TargetInputTokens+spec.RequestedOutputTokens > evidence.ModelMaxLength {
		return PreparedWorkload{}, fmt.Errorf("target input %d plus requested output %d exceeds server max_model_len %d", spec.TargetInputTokens, spec.RequestedOutputTokens, evidence.ModelMaxLength)
	}
	if minimum == spec.TargetInputTokens {
		return b.prepared("", spec, minimum), nil
	}

	delta := spec.TargetInputTokens - minimum
	bestPrompt := ""
	bestCount := minimum
	for _, atom := range deterministicAtoms {
		candidate := strings.Repeat(atom, delta)
		count, _, countErr := b.tokenizer.Count(ctx, candidate)
		if countErr != nil {
			return PreparedWorkload{}, fmt.Errorf("count deterministic workload candidate: %w", countErr)
		}
		if count == spec.TargetInputTokens {
			return b.prepared(candidate, spec, count), nil
		}
		if count > bestCount && count < spec.TargetInputTokens {
			bestPrompt, bestCount = candidate, count
		}
	}

	// Greedily add deterministic power-of-two blocks. Every trial is counted
	// from the complete candidate, so no additive tokenizer assumption is used.
	for size := highestPowerOfTwo(delta); size > 0 && bestCount < spec.TargetInputTokens; size /= 2 {
		for _, fragment := range refinementFragments {
			candidate := bestPrompt + strings.Repeat(fragment, size)
			count, _, countErr := b.tokenizer.Count(ctx, candidate)
			if countErr != nil {
				return PreparedWorkload{}, fmt.Errorf("refine deterministic workload: %w", countErr)
			}
			if count == spec.TargetInputTokens {
				return b.prepared(candidate, spec, count), nil
			}
			if count > bestCount && count < spec.TargetInputTokens {
				bestPrompt, bestCount = candidate, count
			}
		}
	}
	for _, fragment := range refinementFragments {
		candidate := bestPrompt + fragment
		count, _, countErr := b.tokenizer.Count(ctx, candidate)
		if countErr != nil {
			return PreparedWorkload{}, fmt.Errorf("verify workload suffix: %w", countErr)
		}
		if count == spec.TargetInputTokens {
			return b.prepared(candidate, spec, count), nil
		}
	}
	return PreparedWorkload{}, fmt.Errorf("cannot construct exact rendered input target %d; closest deterministic candidate resolved to %d", spec.TargetInputTokens, bestCount)
}

func (b *DeterministicBuilder) prepared(prompt string, spec WorkloadSpec, resolved int) PreparedWorkload {
	digest := sha256.Sum256([]byte(prompt))
	identity := b.tokenizer.Identity()
	return PreparedWorkload{
		Prompt:                prompt,
		Mode:                  ModeTokenLength,
		InputContract:         ContractRenderedChatInput,
		TargetInputTokens:     spec.TargetInputTokens,
		ResolvedInputTokens:   resolved,
		RequestedOutputTokens: spec.RequestedOutputTokens,
		PromptBytes:           len([]byte(prompt)),
		PromptSHA256:          hex.EncodeToString(digest[:]),
		Builder:               &BuilderIdentity{Kind: BuilderKindDeterministic, Version: BuilderVersion},
		Tokenizer:             &identity,
	}
}

func highestPowerOfTwo(value int) int {
	result := 1
	for result <= value/2 {
		result *= 2
	}
	return result
}
