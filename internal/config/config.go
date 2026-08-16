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

// Config is the fully resolved configuration used for one benchmark request.
type Config struct {
	Version  int
	Endpoint Endpoint
	Request  Request
	Runtime  Runtime
	Capture  Capture
}

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

type Runtime struct {
	Timeout time.Duration
}

type Capture struct {
	OutputDir string
}

// Overrides contains only values explicitly supplied on the command line.
// Pointer fields allow zero to remain a meaningful override.
type Overrides struct {
	BaseURL         *string
	APIKeyEnv       *string
	Model           *string
	Prompt          *string
	MaxOutputTokens *int
	Temperature     *float64
	Timeout         *time.Duration
	OutputDir       *string
}

func Default() Config {
	return Config{
		Version: SupportedVersion,
		Request: Request{
			MaxOutputTokens: 64,
			Temperature:     0,
		},
		Runtime: Runtime{Timeout: 120 * time.Second},
		Capture: Capture{OutputDir: "runs"},
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
	if strings.TrimSpace(c.Request.Prompt) == "" {
		return fmt.Errorf("request.prompt is required")
	}
	if c.Request.MaxOutputTokens <= 0 {
		return fmt.Errorf("request.max_output_tokens must be greater than zero")
	}
	if math.IsNaN(c.Request.Temperature) || math.IsInf(c.Request.Temperature, 0) || c.Request.Temperature < 0 {
		return fmt.Errorf("request.temperature must be a finite non-negative number")
	}
	if c.Runtime.Timeout <= 0 {
		return fmt.Errorf("runtime.timeout must be greater than zero")
	}
	if strings.TrimSpace(c.Capture.OutputDir) == "" {
		return fmt.Errorf("capture.output_dir is required")
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
