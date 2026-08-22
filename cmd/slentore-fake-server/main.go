package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/fakeserver"
)

type listenFunc func(network, address string) (net.Listener, error)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, net.Listen))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, listen listenFunc) int {
	config, help, err := parseConfig(args, stderr)
	if help {
		return 0
	}
	if err != nil {
		return 2
	}
	listener, err := listen("tcp", config.Listen)
	if err != nil {
		fmt.Fprintf(stderr, "error: listen on %s: %v\n", config.Listen, err)
		return 1
	}
	defer listener.Close()

	printStartup(stdout, config, listener.Addr().String())
	if err := fakeserver.Serve(ctx, listener, config); err != nil {
		fmt.Fprintf(stderr, "error: serve fake endpoint: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Slentore fake server stopped")
	return 0
}

func parseConfig(args []string, stderr io.Writer) (fakeserver.Config, bool, error) {
	config := fakeserver.DefaultConfig()
	mode := string(config.Mode)
	tokenEvidence := string(config.TokenEvidence)
	flags := flag.NewFlagSet("slentore-fake-server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { printUsage(flags.Output()) }
	flags.StringVar(&config.Listen, "listen", config.Listen, "listen address in host:port form")
	flags.StringVar(&mode, "mode", mode, "response mode")
	flags.DurationVar(&config.HeaderDelay, "header-delay", config.HeaderDelay, "delay before response headers")
	flags.DurationVar(&config.FirstContentDelay, "first-content-delay", config.FirstContentDelay, "delay after headers before generation output")
	flags.DurationVar(&config.ChunkInterval, "chunk-interval", config.ChunkInterval, "delay between content events")
	flags.IntVar(&config.ContentChunks, "content-chunks", config.ContentChunks, "number of content-bearing events")
	flags.DurationVar(&config.UsageDelay, "usage-delay", config.UsageDelay, "delay before the usage event")
	flags.DurationVar(&config.DoneDelay, "done-delay", config.DoneDelay, "delay before the [DONE] event")
	flags.IntVar(&config.PromptTokens, "prompt-tokens", config.PromptTokens, "server-reported prompt token count")
	flags.IntVar(&config.CompletionTokens, "completion-tokens", config.CompletionTokens, "server-reported completion token count")
	flags.StringVar(&tokenEvidence, "token-evidence", tokenEvidence, "token-ID fixture: disabled, singleton, batched, missing, or mismatch")
	flags.BoolVar(&config.TokenizerFixture, "tokenizer-fixture", config.TokenizerFixture, "enable the deterministic byte-token /tokenize test endpoint")
	flags.IntVar(&config.TokenizerMaxModelLength, "tokenizer-max-model-len", config.TokenizerMaxModelLength, "max_model_len returned by the tokenizer fixture")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return config, true, nil
		}
		return config, false, err
	}
	if flags.NArg() != 0 {
		err := fmt.Errorf("unexpected positional arguments: %v", flags.Args())
		fmt.Fprintf(stderr, "error: %v\n", err)
		return config, false, err
	}
	config.Mode = fakeserver.Mode(mode)
	config.TokenEvidence = fakeserver.TokenEvidenceMode(tokenEvidence)
	if err := config.Validate(); err != nil {
		fmt.Fprintf(stderr, "error: invalid configuration: %v\n", err)
		return config, false, err
	}
	return config, false, nil
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: slentore-fake-server [options]")
	fmt.Fprintln(writer, "Serve deterministic OpenAI-compatible streaming fixtures.")
}

func printStartup(writer io.Writer, config fakeserver.Config, address string) {
	fmt.Fprintln(writer, "Slentore fake server")
	fmt.Fprintf(writer, "listen:              %s\n", address)
	fmt.Fprintf(writer, "mode:                %s\n", config.Mode)
	fmt.Fprintf(writer, "header delay:        %s\n", config.HeaderDelay)
	fmt.Fprintf(writer, "first content delay: %s\n", config.FirstContentDelay)
	fmt.Fprintf(writer, "chunk interval:      %s\n", config.ChunkInterval)
	fmt.Fprintf(writer, "content chunks:      %d\n", config.ContentChunks)
	fmt.Fprintf(writer, "usage delay:         %s\n", config.UsageDelay)
	fmt.Fprintf(writer, "DONE delay:          %s\n", config.DoneDelay)
	fmt.Fprintf(writer, "prompt tokens:       %d\n", config.PromptTokens)
	fmt.Fprintf(writer, "completion tokens:   %d\n", config.CompletionTokens)
	fmt.Fprintf(writer, "token evidence:      %s\n", config.TokenEvidence)
	fmt.Fprintf(writer, "tokenizer fixture:   %t\n", config.TokenizerFixture)
}
