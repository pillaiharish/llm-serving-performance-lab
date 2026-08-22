package fakeserver

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type Mode string

const (
	ModeNormal        Mode = "normal"
	ModeNoContent     Mode = "no-content"
	ModeHTTPError     Mode = "http-error"
	ModeMalformedJSON Mode = "malformed-json"
	ModeEOFBeforeDone Mode = "eof-before-done"
	ModeDataAfterDone Mode = "data-after-done"
)

type Config struct {
	Listen                  string
	Mode                    Mode
	HeaderDelay             time.Duration
	FirstContentDelay       time.Duration
	ChunkInterval           time.Duration
	ContentChunks           int
	UsageDelay              time.Duration
	DoneDelay               time.Duration
	PromptTokens            int
	CompletionTokens        int
	TokenizerFixture        bool
	TokenizerMaxModelLength int
}

func DefaultConfig() Config {
	return Config{
		Listen:                  "127.0.0.1:18080",
		Mode:                    ModeNormal,
		HeaderDelay:             50 * time.Millisecond,
		FirstContentDelay:       100 * time.Millisecond,
		ChunkInterval:           20 * time.Millisecond,
		ContentChunks:           4,
		UsageDelay:              10 * time.Millisecond,
		DoneDelay:               10 * time.Millisecond,
		PromptTokens:            16,
		CompletionTokens:        4,
		TokenizerMaxModelLength: 4096,
	}
}

func (c Config) Validate() error {
	if err := validateListenAddress(c.Listen); err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	if !c.Mode.valid() {
		return fmt.Errorf("mode must be one of %s", strings.Join(supportedModeNames(), ", "))
	}
	durations := []struct {
		name  string
		value time.Duration
	}{
		{name: "header delay", value: c.HeaderDelay},
		{name: "first content delay", value: c.FirstContentDelay},
		{name: "chunk interval", value: c.ChunkInterval},
		{name: "usage delay", value: c.UsageDelay},
		{name: "DONE delay", value: c.DoneDelay},
	}
	for _, duration := range durations {
		if duration.value < 0 {
			return fmt.Errorf("%s must not be negative", duration.name)
		}
	}
	if c.ContentChunks < 0 {
		return fmt.Errorf("content chunks must not be negative")
	}
	if c.PromptTokens < 0 {
		return fmt.Errorf("prompt tokens must not be negative")
	}
	if c.CompletionTokens < 0 {
		return fmt.Errorf("completion tokens must not be negative")
	}
	if c.TokenizerMaxModelLength <= 0 {
		return fmt.Errorf("tokenizer max model length must be greater than zero")
	}
	maximumInt := int(^uint(0) >> 1)
	if c.PromptTokens > maximumInt-c.CompletionTokens {
		return fmt.Errorf("total token count overflows int")
	}
	return nil
}

func (c Config) TotalTokens() int {
	return c.PromptTokens + c.CompletionTokens
}

func (m Mode) valid() bool {
	switch m {
	case ModeNormal, ModeNoContent, ModeHTTPError, ModeMalformedJSON, ModeEOFBeforeDone, ModeDataAfterDone:
		return true
	default:
		return false
	}
}

func supportedModeNames() []string {
	return []string{
		string(ModeNormal),
		string(ModeNoContent),
		string(ModeHTTPError),
		string(ModeMalformedJSON),
		string(ModeEOFBeforeDone),
		string(ModeDataAfterDone),
	}
}

func validateListenAddress(address string) error {
	if strings.TrimSpace(address) == "" {
		return fmt.Errorf("host and port are required")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("must use host:port syntax: %w", err)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("host is required")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be an integer from 1 through 65535")
	}
	return nil
}
