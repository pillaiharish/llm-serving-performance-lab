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
	Workload  *fileWorkload `yaml:"workload"`
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

type fileWorkload struct {
	Mode        *WorkloadMode  `yaml:"mode"`
	InputTokens *int           `yaml:"input_tokens"`
	Tokenizer   *fileTokenizer `yaml:"tokenizer"`
}

type fileTokenizer struct {
	Adapter *TokenizerAdapter `yaml:"adapter"`
	URL     *string           `yaml:"url"`
}

type fileRuntime struct {
	Timeout      *string `yaml:"timeout"`
	DrainTimeout *string `yaml:"drain_timeout"`
}

type fileCapture struct {
	OutputDir *string `yaml:"output_dir"`
}

type fileBenchmark struct {
	Mode           *LoadMode     `yaml:"mode"`
	Concurrency    *int          `yaml:"concurrency"`
	Requests       *int          `yaml:"requests"`
	WarmupRequests *int          `yaml:"warmup_requests"`
	OpenLoop       *fileOpenLoop `yaml:"open_loop"`
	Safety         fileSafety    `yaml:"safety"`
}

type fileOpenLoop struct {
	RequestRate *float64 `yaml:"request_rate"`
	Duration    *string  `yaml:"duration"`
	MaxInFlight *int     `yaml:"max_in_flight"`
}

type fileSafety struct {
	MaxConcurrency  *int     `yaml:"max_concurrency"`
	MaxRequests     *int     `yaml:"max_requests"`
	MaxRequestRate  *float64 `yaml:"max_request_rate"`
	MaxInFlight     *int     `yaml:"max_in_flight"`
	MaxInputTokens  *int     `yaml:"max_input_tokens"`
	MaxOutputTokens *int     `yaml:"max_output_tokens"`
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
		resolved.Workload.supplied.Prompt = true
	}
	if raw.Workload != nil {
		if raw.Workload.Mode != nil {
			resolved.Workload.Mode = *raw.Workload.Mode
			resolved.Workload.supplied.Mode = true
		}
		if raw.Workload.InputTokens != nil {
			resolved.Workload.InputTokens = *raw.Workload.InputTokens
			resolved.Workload.supplied.InputTokens = true
		}
		if raw.Workload.Tokenizer != nil {
			if raw.Workload.Tokenizer.Adapter != nil {
				resolved.Workload.Tokenizer.Adapter = *raw.Workload.Tokenizer.Adapter
				resolved.Workload.supplied.TokenizerAdapter = true
			}
			if raw.Workload.Tokenizer.URL != nil {
				resolved.Workload.Tokenizer.URL = *raw.Workload.Tokenizer.URL
				resolved.Workload.supplied.TokenizerURL = true
			}
		}
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
	if raw.Runtime.DrainTimeout != nil {
		drainTimeout, err := time.ParseDuration(*raw.Runtime.DrainTimeout)
		if err != nil {
			return Config{}, fmt.Errorf("runtime.drain_timeout: %w", err)
		}
		resolved.Runtime.DrainTimeout = drainTimeout
	}
	if raw.Capture.OutputDir != nil {
		resolved.Capture.OutputDir = *raw.Capture.OutputDir
	}
	if raw.Benchmark.Concurrency != nil {
		resolved.Benchmark.Concurrency = *raw.Benchmark.Concurrency
		resolved.Benchmark.supplied.Concurrency = true
	}
	if raw.Benchmark.Requests != nil {
		resolved.Benchmark.Requests = *raw.Benchmark.Requests
		resolved.Benchmark.supplied.Requests = true
	}
	if raw.Benchmark.WarmupRequests != nil {
		resolved.Benchmark.WarmupRequests = *raw.Benchmark.WarmupRequests
	}
	if raw.Benchmark.Safety.MaxConcurrency != nil {
		resolved.Benchmark.Safety.MaxConcurrency = *raw.Benchmark.Safety.MaxConcurrency
	}
	if raw.Benchmark.Safety.MaxRequests != nil {
		resolved.Benchmark.Safety.MaxRequests = *raw.Benchmark.Safety.MaxRequests
	}
	if raw.Benchmark.Mode != nil {
		resolved.Benchmark.Mode = *raw.Benchmark.Mode
		resolved.Benchmark.supplied.Mode = true
	}
	if raw.Benchmark.OpenLoop != nil {
		resolved.Benchmark.supplied.OpenLoopBlock = true
		if raw.Benchmark.OpenLoop.RequestRate != nil {
			resolved.Benchmark.OpenLoop.RequestRate = *raw.Benchmark.OpenLoop.RequestRate
			resolved.Benchmark.supplied.RequestRate = true
		}
		if raw.Benchmark.OpenLoop.Duration != nil {
			duration, err := time.ParseDuration(*raw.Benchmark.OpenLoop.Duration)
			if err != nil {
				return Config{}, fmt.Errorf("benchmark.open_loop.duration: %w", err)
			}
			resolved.Benchmark.OpenLoop.Duration = duration
			resolved.Benchmark.supplied.Duration = true
		}
		if raw.Benchmark.OpenLoop.MaxInFlight != nil {
			resolved.Benchmark.OpenLoop.MaxInFlight = *raw.Benchmark.OpenLoop.MaxInFlight
			resolved.Benchmark.supplied.MaxInFlight = true
		}
	}
	if raw.Benchmark.Safety.MaxRequestRate != nil {
		resolved.Benchmark.Safety.MaxRequestRate = *raw.Benchmark.Safety.MaxRequestRate
	}
	if raw.Benchmark.Safety.MaxInFlight != nil {
		resolved.Benchmark.Safety.MaxInFlight = *raw.Benchmark.Safety.MaxInFlight
	}
	if raw.Benchmark.Safety.MaxInputTokens != nil {
		resolved.Benchmark.Safety.MaxInputTokens = *raw.Benchmark.Safety.MaxInputTokens
	}
	if raw.Benchmark.Safety.MaxOutputTokens != nil {
		resolved.Benchmark.Safety.MaxOutputTokens = *raw.Benchmark.Safety.MaxOutputTokens
	}

	return resolved, nil
}
