package fakeserver

import "encoding/json"

type chatCompletionRequest struct {
	Model         string            `json:"model"`
	Messages      []json.RawMessage `json:"messages"`
	Stream        bool              `json:"stream"`
	StreamOptions streamOptions     `json:"stream_options"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type streamChunk struct {
	Choices []streamChoice `json:"choices"`
	Usage   *streamUsage   `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int               `json:"index"`
	Delta        map[string]string `json:"delta"`
	FinishReason *string           `json:"finish_reason,omitempty"`
}

type streamUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type apiErrorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type tokenizeRequest struct {
	Model               string            `json:"model"`
	Messages            []tokenizeMessage `json:"messages"`
	AddGenerationPrompt bool              `json:"add_generation_prompt"`
	AddSpecialTokens    bool              `json:"add_special_tokens"`
	ReturnTokenStrings  bool              `json:"return_token_strs"`
}

type tokenizeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type tokenizeResponse struct {
	Count          int   `json:"count"`
	MaxModelLength int   `json:"max_model_len"`
	Tokens         []int `json:"tokens"`
}
