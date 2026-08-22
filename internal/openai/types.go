package openai

type chatCompletionRequest struct {
	Model          string        `json:"model"`
	Messages       []message     `json:"messages"`
	MaxTokens      int           `json:"max_tokens"`
	Temperature    float64       `json:"temperature"`
	Stream         bool          `json:"stream"`
	StreamOptions  streamOptions `json:"stream_options"`
	ReturnTokenIDs *bool         `json:"return_token_ids,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type streamChunk struct {
	Choices []choice    `json:"choices"`
	Usage   *tokenUsage `json:"usage"`
}

type choice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
	TokenIDs     *[]int  `json:"token_ids"`
}

type delta struct {
	Role    string  `json:"role"`
	Content *string `json:"content"`
}

type tokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
