package config

import (
	"fmt"
	"io"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

type fileConfig struct {
	Version   *int          `yaml:"version"`
	Endpoint  fileEndpoint  `yaml:"endpoint"`
	Request   fileRequest   `yaml:"request"`
	Runtime   fileRuntime   `yaml:"runtime"`
	Capture   fileCapture   `yaml:"capture"`
	Benchmark fileBenchmark `yaml:"benchmark"`
}

type fileEndpoint struct {
	BaseURL   *string `yaml:"base_url"`
	APIKeyEnv *string `yaml:"api_key_env"`
	Model     *string `yaml:"model"`
}

type fileRequest struct {
	Prompt          *string  `yaml:"prompt"`
	MaxOutputTokens *int     `yaml:"max_output_tokens"`
	Temperature     *float64 `yaml:"temperature"`
}

type fileRuntime struct {
	Timeout *string `yaml:"timeout"`
}

type fileCapture struct {
	OutputDir *string `yaml:"output_dir"`
}

type fileBenchmark struct {
	Concurrency *int       `yaml:"concurrency"`
	Requests    *int       `yaml:"requests"`
	Safety      fileSafety `yaml:"safety"`
}

type fileSafety struct {
	MaxConcurrency *int `yaml:"max_concurrency"`
	MaxRequests    *int `yaml:"max_requests"`
}

// Load applies a YAML file, when provided, over the built-in defaults.
func Load(path string) (Config, error) {
	resolved := Default()
	if path == "" {
		return resolved, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)

	var raw fileConfig
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if raw.Version == nil {
		return Config{}, fmt.Errorf("config version is required")
	}
	if *raw.Version != SupportedVersion {
		return Config{}, fmt.Errorf("config version must be %d", SupportedVersion)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("config must contain exactly one YAML document")
		}
		return Config{}, fmt.Errorf("decode trailing config document: %w", err)
	}

	resolved.Version = *raw.Version
	if raw.Endpoint.BaseURL != nil {
		resolved.Endpoint.BaseURL = *raw.Endpoint.BaseURL
	}
	if raw.Endpoint.APIKeyEnv != nil {
		resolved.Endpoint.APIKeyEnv = *raw.Endpoint.APIKeyEnv
	}
	if raw.Endpoint.Model != nil {
		resolved.Endpoint.Model = *raw.Endpoint.Model
	}
	if raw.Request.Prompt != nil {
		resolved.Request.Prompt = *raw.Request.Prompt
	}
	if raw.Request.MaxOutputTokens != nil {
		resolved.Request.MaxOutputTokens = *raw.Request.MaxOutputTokens
	}
	if raw.Request.Temperature != nil {
		resolved.Request.Temperature = *raw.Request.Temperature
	}
	if raw.Runtime.Timeout != nil {
		timeout, err := time.ParseDuration(*raw.Runtime.Timeout)
		if err != nil {
			return Config{}, fmt.Errorf("runtime.timeout: %w", err)
		}
		resolved.Runtime.Timeout = timeout
	}
	if raw.Capture.OutputDir != nil {
		resolved.Capture.OutputDir = *raw.Capture.OutputDir
	}
	if raw.Benchmark.Concurrency != nil {
		resolved.Benchmark.Concurrency = *raw.Benchmark.Concurrency
	}
	if raw.Benchmark.Requests != nil {
		resolved.Benchmark.Requests = *raw.Benchmark.Requests
	}
	if raw.Benchmark.Safety.MaxConcurrency != nil {
		resolved.Benchmark.Safety.MaxConcurrency = *raw.Benchmark.Safety.MaxConcurrency
	}
	if raw.Benchmark.Safety.MaxRequests != nil {
		resolved.Benchmark.Safety.MaxRequests = *raw.Benchmark.Safety.MaxRequests
	}

	return resolved, nil
}
