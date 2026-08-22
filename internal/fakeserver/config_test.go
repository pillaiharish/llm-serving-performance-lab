package fakeserver

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	got := DefaultConfig()
	if got.Listen != "127.0.0.1:18080" || got.Mode != ModeNormal || got.TokenEvidence != TokenEvidenceDisabled || got.HeaderDelay != 50*time.Millisecond || got.FirstContentDelay != 100*time.Millisecond || got.ChunkInterval != 20*time.Millisecond || got.ContentChunks != 4 || got.UsageDelay != 10*time.Millisecond || got.DoneDelay != 10*time.Millisecond || got.PromptTokens != 16 || got.CompletionTokens != 4 {
		t.Fatalf("unexpected defaults: %+v", got)
	}
	if got.TotalTokens() != 20 {
		t.Fatalf("total tokens = %d, want 20", got.TotalTokens())
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate defaults: %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		alter     func(*Config)
		wantError string
	}{
		{name: "missing listen", alter: func(c *Config) { c.Listen = "" }, wantError: "listen address"},
		{name: "missing port", alter: func(c *Config) { c.Listen = "127.0.0.1" }, wantError: "host:port"},
		{name: "missing host", alter: func(c *Config) { c.Listen = ":18080" }, wantError: "host is required"},
		{name: "zero port", alter: func(c *Config) { c.Listen = "127.0.0.1:0" }, wantError: "port must"},
		{name: "invalid port", alter: func(c *Config) { c.Listen = "127.0.0.1:not-a-port" }, wantError: "port must"},
		{name: "unknown mode", alter: func(c *Config) { c.Mode = "unknown" }, wantError: "mode must"},
		{name: "unknown token evidence", alter: func(c *Config) { c.TokenEvidence = "unknown" }, wantError: "token evidence"},
		{name: "singleton count mismatch", alter: func(c *Config) { c.TokenEvidence = TokenEvidenceSingleton; c.CompletionTokens = 3 }, wantError: "equal content chunks"},
		{name: "batched too short", alter: func(c *Config) { c.TokenEvidence = TokenEvidenceBatched; c.ContentChunks = 1; c.CompletionTokens = 1 }, wantError: "at least two"},
		{name: "negative header delay", alter: func(c *Config) { c.HeaderDelay = -time.Nanosecond }, wantError: "header delay"},
		{name: "negative first content delay", alter: func(c *Config) { c.FirstContentDelay = -time.Nanosecond }, wantError: "first content delay"},
		{name: "negative chunk interval", alter: func(c *Config) { c.ChunkInterval = -time.Nanosecond }, wantError: "chunk interval"},
		{name: "negative usage delay", alter: func(c *Config) { c.UsageDelay = -time.Nanosecond }, wantError: "usage delay"},
		{name: "negative done delay", alter: func(c *Config) { c.DoneDelay = -time.Nanosecond }, wantError: "DONE delay"},
		{name: "negative content chunks", alter: func(c *Config) { c.ContentChunks = -1 }, wantError: "content chunks"},
		{name: "negative prompt tokens", alter: func(c *Config) { c.PromptTokens = -1 }, wantError: "prompt tokens"},
		{name: "negative completion tokens", alter: func(c *Config) { c.CompletionTokens = -1 }, wantError: "completion tokens"},
		{name: "token overflow", alter: func(c *Config) { c.PromptTokens = int(^uint(0) >> 1); c.CompletionTokens = 1 }, wantError: "overflows"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig()
			test.alter(&config)
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Validate error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func TestConfigAcceptsTokenEvidenceFixtures(t *testing.T) {
	for _, mode := range []TokenEvidenceMode{TokenEvidenceDisabled, TokenEvidenceSingleton, TokenEvidenceBatched, TokenEvidenceMissing, TokenEvidenceMismatch} {
		t.Run(string(mode), func(t *testing.T) {
			config := DefaultConfig()
			config.TokenEvidence = mode
			if err := config.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestConfigAcceptsZerosAndSupportedModes(t *testing.T) {
	for _, mode := range []Mode{ModeNormal, ModeNoContent, ModeHTTPError, ModeMalformedJSON, ModeEOFBeforeDone, ModeDataAfterDone} {
		t.Run(string(mode), func(t *testing.T) {
			config := DefaultConfig()
			config.Mode = mode
			config.HeaderDelay = 0
			config.FirstContentDelay = 0
			config.ChunkInterval = 0
			config.ContentChunks = 0
			config.UsageDelay = 0
			config.DoneDelay = 0
			config.PromptTokens = 0
			config.CompletionTokens = 0
			if err := config.Validate(); err != nil {
				t.Fatalf("Validate zero config: %v", err)
			}
		})
	}
}
