package workload

import "context"

const (
	ModePrompt      = "prompt"
	ModeTokenLength = "token_length"

	ContractRenderedChatInput = "rendered_chat_input"
	BuilderKindDeterministic  = "deterministic_fixture"
	BuilderVersion            = "1"
)

type TokenizationEvidence struct {
	Count          int
	ModelMaxLength int
}

type TokenizerIdentity struct {
	Adapter                     string  `json:"adapter"`
	AdapterVersion              string  `json:"adapter_version"`
	Contract                    string  `json:"contract"`
	Model                       string  `json:"model"`
	Source                      string  `json:"source"`
	Revision                    *string `json:"revision"`
	VocabularySHA256            *string `json:"vocabulary_sha256"`
	BehavioralFingerprintSHA256 string  `json:"behavioral_fingerprint_sha256"`
	ModelMaxLength              int     `json:"model_max_length"`
}

type Tokenizer interface {
	Encode(context.Context, string) ([]int, TokenizationEvidence, error)
	Count(context.Context, string) (int, TokenizationEvidence, error)
	Identity() TokenizerIdentity
}

type WorkloadSpec struct {
	TargetInputTokens     int
	RequestedOutputTokens int
	MaxInputTokens        int
}

type BuilderIdentity struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
}

type PreparedWorkload struct {
	Prompt string

	Mode                  string
	InputContract         string
	TargetInputTokens     int
	ResolvedInputTokens   int
	RequestedOutputTokens int
	PromptBytes           int
	PromptSHA256          string
	Builder               *BuilderIdentity
	Tokenizer             *TokenizerIdentity
}

type WorkloadBuilder interface {
	Build(context.Context, WorkloadSpec) (PreparedWorkload, error)
}
