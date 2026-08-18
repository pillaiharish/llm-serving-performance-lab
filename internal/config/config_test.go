package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultAndOverridesPreserveExplicitZero(t *testing.T) {
	resolved := Default()
	if resolved.Request.MaxOutputTokens != 64 || resolved.Runtime.Timeout != 120*time.Second || resolved.Capture.OutputDir != "runs" || resolved.Benchmark.Concurrency != 1 || resolved.Benchmark.Requests != 1 || resolved.Benchmark.Safety.MaxConcurrency != 256 || resolved.Benchmark.Safety.MaxRequests != 10000 {
		t.Fatalf("unexpected defaults: %+v", resolved)
	}

	temperature := 0.0
	apiKeyEnv := ""
	concurrency := 0
	requests := 0
	maxConcurrency := 0
	maxRequests := 0
	resolved.Endpoint.APIKeyEnv = "FROM_YAML"
	resolved.Request.Temperature = 1.25
	resolved.ApplyOverrides(Overrides{Temperature: &temperature, APIKeyEnv: &apiKeyEnv, Concurrency: &concurrency, Requests: &requests, MaxConcurrency: &maxConcurrency, MaxRequests: &maxRequests})
	if resolved.Request.Temperature != 0 || resolved.Endpoint.APIKeyEnv != "" || resolved.Benchmark.Concurrency != 0 || resolved.Benchmark.Requests != 0 || resolved.Benchmark.Safety.MaxConcurrency != 0 || resolved.Benchmark.Safety.MaxRequests != 0 {
		t.Fatalf("explicit zero/empty overrides not applied: %+v", resolved)
	}
}

func TestLoadMergesYAMLOverDefaults(t *testing.T) {
	path := writeConfig(t, `version: 1
endpoint:
  base_url: http://127.0.0.1:8000/v1
  model: test-model
request:
  prompt: hello
  temperature: 0.25
runtime:
  timeout: 3s
benchmark:
  concurrency: 4
  requests: 20
  safety:
    max_concurrency: 8
    max_requests: 25
`)
	resolved, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if resolved.Request.MaxOutputTokens != 64 || resolved.Capture.OutputDir != "runs" {
		t.Fatalf("defaults were not preserved: %+v", resolved)
	}
	if resolved.Request.Temperature != 0.25 || resolved.Runtime.Timeout != 3*time.Second || resolved.Benchmark.Concurrency != 4 || resolved.Benchmark.Requests != 20 || resolved.Benchmark.Safety.MaxConcurrency != 8 || resolved.Benchmark.Safety.MaxRequests != 25 {
		t.Fatalf("YAML values were not applied: %+v", resolved)
	}
	if err := resolved.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestLoadRejectsInvalidYAMLContracts(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "missing version", content: "endpoint: {}\n", want: "version is required"},
		{name: "unsupported version", content: "version: 2\n", want: "version must be 1"},
		{name: "unknown field", content: "version: 1\nunknown: true\n", want: "field unknown not found"},
		{name: "invalid duration", content: "version: 1\nruntime:\n  timeout: soon\n", want: "runtime.timeout"},
		{name: "multiple documents", content: "version: 1\n---\nversion: 1\n", want: "exactly one YAML document"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	valid := Default()
	valid.Endpoint.BaseURL = "http://localhost:8000/v1"
	valid.Endpoint.Model = "model"
	valid.Request.Prompt = "prompt"

	tests := []struct {
		name  string
		alter func(*Config)
		want  string
	}{
		{name: "missing model", alter: func(value *Config) { value.Endpoint.Model = "" }, want: "model is required"},
		{name: "blank prompt", alter: func(value *Config) { value.Request.Prompt = " \n" }, want: "prompt is required"},
		{name: "URL userinfo", alter: func(value *Config) { value.Endpoint.BaseURL = "https://secret@example.com/v1" }, want: "userinfo"},
		{name: "URL query", alter: func(value *Config) { value.Endpoint.BaseURL = "https://example.com/v1?key=secret" }, want: "query"},
		{name: "URL fragment", alter: func(value *Config) { value.Endpoint.BaseURL = "https://example.com/v1#secret" }, want: "fragment"},
		{name: "invalid scheme", alter: func(value *Config) { value.Endpoint.BaseURL = "ftp://example.com/v1" }, want: "scheme"},
		{name: "invalid key env", alter: func(value *Config) { value.Endpoint.APIKeyEnv = "BAD-NAME" }, want: "environment variable name"},
		{name: "zero tokens", alter: func(value *Config) { value.Request.MaxOutputTokens = 0 }, want: "greater than zero"},
		{name: "zero timeout", alter: func(value *Config) { value.Runtime.Timeout = 0 }, want: "greater than zero"},
		{name: "zero concurrency", alter: func(value *Config) { value.Benchmark.Concurrency = 0 }, want: "benchmark.concurrency"},
		{name: "zero requests", alter: func(value *Config) { value.Benchmark.Requests = 0 }, want: "benchmark.requests"},
		{name: "zero max concurrency", alter: func(value *Config) { value.Benchmark.Safety.MaxConcurrency = 0 }, want: "max_concurrency"},
		{name: "zero max requests", alter: func(value *Config) { value.Benchmark.Safety.MaxRequests = 0 }, want: "max_requests"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.alter(&candidate)
			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestResolveAPIKey(t *testing.T) {
	if value, err := ResolveAPIKey("", nil); err != nil || value != "" {
		t.Fatalf("optional key = %q, err = %v", value, err)
	}
	const secret = "never-print-this-value"
	value, err := ResolveAPIKey("TEST_API_KEY", func(name string) (string, bool) {
		return secret, name == "TEST_API_KEY"
	})
	if err != nil || value != secret {
		t.Fatalf("resolved key = %q, err = %v", value, err)
	}
	_, err = ResolveAPIKey("MISSING_API_KEY", func(string) (string, bool) { return "", false })
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "MISSING_API_KEY") {
		t.Fatalf("unexpected missing-key error: %v", err)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
