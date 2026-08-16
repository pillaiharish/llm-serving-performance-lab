package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/fakeserver"
)

func TestParseConfigDefaultsAndOverrides(t *testing.T) {
	var stderr bytes.Buffer
	defaults, help, err := parseConfig(nil, &stderr)
	if err != nil || help || stderr.Len() != 0 {
		t.Fatalf("defaults: config=%+v help=%v err=%v stderr=%q", defaults, help, err, stderr.String())
	}
	if defaults != fakeserver.DefaultConfig() {
		t.Fatalf("defaults = %+v, want %+v", defaults, fakeserver.DefaultConfig())
	}

	config, help, err := parseConfig([]string{
		"--listen", "localhost:19090",
		"--mode", "no-content",
		"--header-delay", "1ms",
		"--first-content-delay", "2ms",
		"--chunk-interval", "3ms",
		"--content-chunks", "0",
		"--usage-delay", "4ms",
		"--done-delay", "5ms",
		"--prompt-tokens", "0",
		"--completion-tokens", "0",
	}, &stderr)
	if err != nil || help {
		t.Fatalf("overrides: config=%+v help=%v err=%v stderr=%q", config, help, err, stderr.String())
	}
	if config.Listen != "localhost:19090" || config.Mode != fakeserver.ModeNoContent || config.HeaderDelay != time.Millisecond || config.FirstContentDelay != 2*time.Millisecond || config.ChunkInterval != 3*time.Millisecond || config.ContentChunks != 0 || config.UsageDelay != 4*time.Millisecond || config.DoneDelay != 5*time.Millisecond || config.PromptTokens != 0 || config.CompletionTokens != 0 {
		t.Fatalf("overrides = %+v", config)
	}
}

func TestParseConfigErrorsAndHelp(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantHelp bool
		wantText string
	}{
		{name: "help", args: []string{"--help"}, wantHelp: true, wantText: "Usage:"},
		{name: "unknown mode", args: []string{"--mode", "unknown"}, wantText: "mode must"},
		{name: "negative delay", args: []string{"--header-delay", "-1ms"}, wantText: "must not be negative"},
		{name: "negative chunks", args: []string{"--content-chunks", "-1"}, wantText: "content chunks"},
		{name: "negative prompt tokens", args: []string{"--prompt-tokens", "-1"}, wantText: "prompt tokens"},
		{name: "invalid address", args: []string{"--listen", "bad-address"}, wantText: "host:port"},
		{name: "positional", args: []string{"extra"}, wantText: "unexpected positional"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, help, err := parseConfig(test.args, &stderr)
			if test.wantHelp {
				if err != nil || !help || !strings.Contains(stderr.String(), test.wantText) {
					t.Fatalf("help=%v err=%v stderr=%q", help, err, stderr.String())
				}
				return
			}
			if err == nil || help || !strings.Contains(stderr.String(), test.wantText) {
				t.Fatalf("help=%v err=%v stderr=%q", help, err, stderr.String())
			}
		})
	}
}

func TestRunExitCodesBindFailureAndShutdown(t *testing.T) {
	t.Run("configuration error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		exitCode := run(context.Background(), []string{"--mode", "bad"}, &stdout, &stderr, net.Listen)
		if exitCode != 2 || stdout.Len() != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
		}
	})

	t.Run("bind failure", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		exitCode := run(context.Background(), nil, &stdout, &stderr, func(string, string) (net.Listener, error) {
			return nil, errors.New("address unavailable")
		})
		if exitCode != 1 || !strings.Contains(stderr.String(), "address unavailable") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
		}
	})

	t.Run("clean shutdown and safe summary", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("Listen: %v", err)
		}
		var stdout, stderr bytes.Buffer
		cancel()
		exitCode := run(ctx, []string{"--mode", "normal"}, &stdout, &stderr, func(string, string) (net.Listener, error) {
			return listener, nil
		})
		if exitCode != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Slentore fake server") || !strings.Contains(stdout.String(), "stopped") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
		}
		for _, forbidden := range []string{"private fixture prompt", "Authorization", "private-api-secret"} {
			if strings.Contains(stdout.String(), forbidden) {
				t.Fatalf("startup output contains forbidden value %q: %q", forbidden, stdout.String())
			}
		}
	})
}
