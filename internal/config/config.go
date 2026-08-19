package config

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const SupportedVersion = 1

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config is the fully resolved configuration used for one benchmark run.
type Config struct {
	Version   int
	Endpoint  Endpoint
	Request   Request
	Workload  Workload
	Runtime   Runtime
	Capture   Capture
	Benchmark Benchmark
}

type WorkloadMode string

const (
	WorkloadModePrompt      WorkloadMode = "prompt"
	WorkloadModeTokenLength WorkloadMode = "token_length"
)

type TokenizerAdapter string

const TokenizerAdapterVLLM TokenizerAdapter = "vllm"

type LoadMode string

const (
	LoadModeClosedLoop LoadMode = "closed_loop"
	LoadModeOpenLoop   LoadMode = "open_loop"
)

type Endpoint struct {
	BaseURL   string
	APIKeyEnv string
	Model     string
}

type Request struct {
	Prompt          string
	MaxOutputTokens int
	Temperature     float64
}

type Workload struct {
	Mode        WorkloadMode
	InputTokens int
	Tokenizer   Tokenizer
	supplied    workloadFields
}

type Tokenizer struct {
	Adapter TokenizerAdapter
	URL     string
}

type workloadFields struct {
	Mode             bool
	InputTokens      bool
	TokenizerAdapter bool
	TokenizerURL     bool
	Prompt           bool
}

type Runtime struct {
	Timeout      time.Duration
	DrainTimeout time.Duration
}

type Capture struct {
	OutputDir string
}

type Benchmark struct {
	Mode           LoadMode
	Concurrency    int
	Requests       int
	WarmupRequests int
	OpenLoop       OpenLoop
	Safety         Safety
	supplied       benchmarkFields
}

type OpenLoop struct {
	RequestRate float64
	Duration    time.Duration
	MaxInFlight int
}

type Safety struct {
	MaxConcurrency  int
	MaxRequests     int
	MaxRequestRate  float64
	MaxInFlight     int
	MaxInputTokens  int
	MaxOutputTokens int
}

type benchmarkFields struct {
	Mode          bool
	Concurrency   bool
	Requests      bool
	RequestRate   bool
	Duration      bool
	MaxInFlight   bool
	OpenLoopBlock bool
}

// Overrides contains only values explicitly supplied on the command line.
// Pointer fields allow zero to remain a meaningful override.
type Overrides struct {
	BaseURL                *string
	APIKeyEnv              *string
	Model                  *string
	Prompt                 *string
	WorkloadMode           *WorkloadMode
	InputTokens            *int
	TokenizerAdapter       *TokenizerAdapter
	TokenizerURL           *string
	MaxOutputTokens        *int
	Temperature            *float64
	Timeout                *time.Duration
	OutputDir              *string
	Concurrency            *int
	Requests               *int
	MaxConcurrency         *int
	MaxRequests            *int
	WarmupRequests         *int
	DrainTimeout           *time.Duration
	Mode                   *LoadMode
	RequestRate            *float64
	Duration               *time.Duration
	MaxInFlight            *int
	MaxRequestRate         *float64
	MaxInFlightCeiling     *int
	MaxInputTokens         *int
	MaxOutputTokensCeiling *int
}

func Default() Config {
	return Config{
		Version: SupportedVersion,
		Request: Request{
			MaxOutputTokens: 64,
			Temperature:     0,
		},
		Workload: Workload{Mode: WorkloadModePrompt},
		Runtime:  Runtime{Timeout: 120 * time.Second, DrainTimeout: 120 * time.Second},
		Capture:  Capture{OutputDir: "runs"},
		Benchmark: Benchmark{
			Mode:        LoadModeClosedLoop,
			Concurrency: 1,
			Requests:    1,
			Safety: Safety{
				MaxConcurrency:  256,
				MaxRequests:     10000,
				MaxRequestRate:  10000,
				MaxInFlight:     256,
				MaxInputTokens:  131072,
				MaxOutputTokens: 32768,
			},
		},
	}
}

func (c *Config) ApplyOverrides(overrides Overrides) {
	if overrides.BaseURL != nil {
		c.Endpoint.BaseURL = *overrides.BaseURL
	}
	if overrides.APIKeyEnv != nil {
		c.Endpoint.APIKeyEnv = *overrides.APIKeyEnv
	}
	if overrides.Model != nil {
		c.Endpoint.Model = *overrides.Model
	}
	if overrides.Prompt != nil {
		c.Request.Prompt = *overrides.Prompt
		c.Workload.supplied.Prompt = true
	}
	if overrides.WorkloadMode != nil {
		c.Workload.Mode = *overrides.WorkloadMode
		c.Workload.supplied.Mode = true
	}
	if overrides.InputTokens != nil {
		c.Workload.InputTokens = *overrides.InputTokens
		c.Workload.supplied.InputTokens = true
	}
	if overrides.TokenizerAdapter != nil {
		c.Workload.Tokenizer.Adapter = *overrides.TokenizerAdapter
		c.Workload.supplied.TokenizerAdapter = true
	}
	if overrides.TokenizerURL != nil {
		c.Workload.Tokenizer.URL = *overrides.TokenizerURL
		c.Workload.supplied.TokenizerURL = true
	}
	if overrides.MaxOutputTokens != nil {
		c.Request.MaxOutputTokens = *overrides.MaxOutputTokens
	}
	if overrides.Temperature != nil {
		c.Request.Temperature = *overrides.Temperature
	}
	if overrides.Timeout != nil {
		c.Runtime.Timeout = *overrides.Timeout
	}
	if overrides.OutputDir != nil {
		c.Capture.OutputDir = *overrides.OutputDir
	}
	if overrides.Concurrency != nil {
		c.Benchmark.Concurrency = *overrides.Concurrency
		c.Benchmark.supplied.Concurrency = true
	}
	if overrides.Requests != nil {
		c.Benchmark.Requests = *overrides.Requests
		c.Benchmark.supplied.Requests = true
	}
	if overrides.MaxConcurrency != nil {
		c.Benchmark.Safety.MaxConcurrency = *overrides.MaxConcurrency
	}
	if overrides.MaxRequests != nil {
		c.Benchmark.Safety.MaxRequests = *overrides.MaxRequests
	}
	if overrides.WarmupRequests != nil {
		c.Benchmark.WarmupRequests = *overrides.WarmupRequests
	}
	if overrides.DrainTimeout != nil {
		c.Runtime.DrainTimeout = *overrides.DrainTimeout
	}
	if overrides.Mode != nil {
		c.Benchmark.Mode = *overrides.Mode
		c.Benchmark.supplied.Mode = true
	}
	if overrides.RequestRate != nil {
		c.Benchmark.OpenLoop.RequestRate = *overrides.RequestRate
		c.Benchmark.supplied.RequestRate = true
		c.Benchmark.supplied.OpenLoopBlock = true
	}
	if overrides.Duration != nil {
		c.Benchmark.OpenLoop.Duration = *overrides.Duration
		c.Benchmark.supplied.Duration = true
		c.Benchmark.supplied.OpenLoopBlock = true
	}
	if overrides.MaxInFlight != nil {
		c.Benchmark.OpenLoop.MaxInFlight = *overrides.MaxInFlight
		c.Benchmark.supplied.MaxInFlight = true
		c.Benchmark.supplied.OpenLoopBlock = true
	}
	if overrides.MaxRequestRate != nil {
		c.Benchmark.Safety.MaxRequestRate = *overrides.MaxRequestRate
	}
	if overrides.MaxInFlightCeiling != nil {
		c.Benchmark.Safety.MaxInFlight = *overrides.MaxInFlightCeiling
	}
	if overrides.MaxInputTokens != nil {
		c.Benchmark.Safety.MaxInputTokens = *overrides.MaxInputTokens
	}
	if overrides.MaxOutputTokensCeiling != nil {
		c.Benchmark.Safety.MaxOutputTokens = *overrides.MaxOutputTokensCeiling
	}
}

func (c Config) Validate() error {
	if c.Version != SupportedVersion {
		return fmt.Errorf("config version must be %d", SupportedVersion)
	}
	if strings.TrimSpace(c.Endpoint.BaseURL) == "" {
		return fmt.Errorf("endpoint.base_url is required")
	}
	if err := validateBaseURL(c.Endpoint.BaseURL); err != nil {
		return fmt.Errorf("endpoint.base_url: %w", err)
	}
	if c.Endpoint.APIKeyEnv != "" && !environmentNamePattern.MatchString(c.Endpoint.APIKeyEnv) {
		return fmt.Errorf("endpoint.api_key_env must be a valid environment variable name")
	}
	if strings.TrimSpace(c.Endpoint.Model) == "" {
		return fmt.Errorf("endpoint.model is required")
	}
	if c.Request.MaxOutputTokens <= 0 {
		return fmt.Errorf("request.max_output_tokens must be greater than zero")
	}
	if c.Benchmark.Safety.MaxInputTokens <= 0 {
		return fmt.Errorf("benchmark.safety.max_input_tokens must be greater than zero")
	}
	if c.Benchmark.Safety.MaxOutputTokens <= 0 {
		return fmt.Errorf("benchmark.safety.max_output_tokens must be greater than zero")
	}
	if c.Request.MaxOutputTokens > c.Benchmark.Safety.MaxOutputTokens {
		return fmt.Errorf("request.max_output_tokens exceeds benchmark.safety.max_output_tokens")
	}
	switch c.Workload.Mode {
	case WorkloadModePrompt:
		if c.Workload.supplied.InputTokens || c.Workload.supplied.TokenizerAdapter || c.Workload.supplied.TokenizerURL {
			return fmt.Errorf("token-length workload settings are incompatible with workload.mode prompt")
		}
		if strings.TrimSpace(c.Request.Prompt) == "" {
			return fmt.Errorf("request.prompt is required for workload.mode prompt")
		}
	case WorkloadModeTokenLength:
		if c.Workload.supplied.Prompt {
			return fmt.Errorf("request.prompt is incompatible with workload.mode token_length")
		}
		if c.Workload.InputTokens <= 0 {
			return fmt.Errorf("workload.input_tokens must be greater than zero")
		}
		if c.Workload.InputTokens > c.Benchmark.Safety.MaxInputTokens {
			return fmt.Errorf("workload.input_tokens exceeds benchmark.safety.max_input_tokens")
		}
		if c.Workload.Tokenizer.Adapter != TokenizerAdapterVLLM {
			return fmt.Errorf("workload.tokenizer.adapter must be vllm")
		}
		if strings.TrimSpace(c.Workload.Tokenizer.URL) == "" {
			return fmt.Errorf("workload.tokenizer.url is required")
		}
		if err := validateBaseURL(c.Workload.Tokenizer.URL); err != nil {
			return fmt.Errorf("workload.tokenizer.url: %w", err)
		}
		if c.Workload.InputTokens > int(^uint(0)>>1)-c.Request.MaxOutputTokens {
			return fmt.Errorf("workload input and requested output token total overflows int")
		}
	default:
		return fmt.Errorf("workload.mode must be prompt or token_length")
	}
	if math.IsNaN(c.Request.Temperature) || math.IsInf(c.Request.Temperature, 0) || c.Request.Temperature < 0 {
		return fmt.Errorf("request.temperature must be a finite non-negative number")
	}
	if c.Runtime.Timeout <= 0 {
		return fmt.Errorf("runtime.timeout must be greater than zero")
	}
	if c.Runtime.DrainTimeout <= 0 {
		return fmt.Errorf("runtime.drain_timeout must be greater than zero")
	}
	if strings.TrimSpace(c.Capture.OutputDir) == "" {
		return fmt.Errorf("capture.output_dir is required")
	}
	if c.Benchmark.WarmupRequests < 0 {
		return fmt.Errorf("benchmark.warmup_requests must not be negative")
	}
	if c.Benchmark.Safety.MaxConcurrency <= 0 {
		return fmt.Errorf("benchmark.safety.max_concurrency must be greater than zero")
	}
	if c.Benchmark.Safety.MaxRequests <= 0 {
		return fmt.Errorf("benchmark.safety.max_requests must be greater than zero")
	}
	if math.IsNaN(c.Benchmark.Safety.MaxRequestRate) || math.IsInf(c.Benchmark.Safety.MaxRequestRate, 0) || c.Benchmark.Safety.MaxRequestRate <= 0 {
		return fmt.Errorf("benchmark.safety.max_request_rate must be a finite number greater than zero")
	}
	if c.Benchmark.Safety.MaxInFlight <= 0 {
		return fmt.Errorf("benchmark.safety.max_in_flight must be greater than zero")
	}
	switch c.Benchmark.Mode {
	case LoadModeClosedLoop:
		if c.Benchmark.supplied.OpenLoopBlock {
			return fmt.Errorf("benchmark.open_loop settings are incompatible with benchmark.mode closed_loop")
		}
		if c.Benchmark.Concurrency <= 0 {
			return fmt.Errorf("benchmark.concurrency must be greater than zero")
		}
		if c.Benchmark.Requests <= 0 {
			return fmt.Errorf("benchmark.requests must be greater than zero")
		}
	case LoadModeOpenLoop:
		if c.Benchmark.supplied.Concurrency || c.Benchmark.supplied.Requests {
			return fmt.Errorf("benchmark.concurrency and benchmark.requests are incompatible with benchmark.mode open_loop")
		}
		if math.IsNaN(c.Benchmark.OpenLoop.RequestRate) || math.IsInf(c.Benchmark.OpenLoop.RequestRate, 0) || c.Benchmark.OpenLoop.RequestRate <= 0 {
			return fmt.Errorf("benchmark.open_loop.request_rate must be a finite number greater than zero")
		}
		if c.Benchmark.OpenLoop.Duration <= 0 {
			return fmt.Errorf("benchmark.open_loop.duration must be greater than zero")
		}
		if c.Benchmark.OpenLoop.MaxInFlight <= 0 {
			return fmt.Errorf("benchmark.open_loop.max_in_flight must be greater than zero")
		}
	default:
		return fmt.Errorf("benchmark.mode must be closed_loop or open_loop")
	}
	return nil
}

func validateBaseURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("host is required")
	}
	if parsed.User != nil {
		return fmt.Errorf("userinfo is not allowed")
	}
	if parsed.RawQuery != "" {
		return fmt.Errorf("query parameters are not allowed")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("fragments are not allowed")
	}
	return nil
}

// ResolveAPIKey returns the secret named by api_key_env without including the
// secret value in any error.
func ResolveAPIKey(apiKeyEnv string, lookup func(string) (string, bool)) (string, error) {
	if apiKeyEnv == "" {
		return "", nil
	}
	value, ok := lookup(apiKeyEnv)
	if !ok || value == "" {
		return "", fmt.Errorf("environment variable %s is not set or is empty", apiKeyEnv)
	}
	return value, nil
}
