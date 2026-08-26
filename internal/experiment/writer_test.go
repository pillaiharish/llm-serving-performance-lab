package experiment

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmarkexec"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
)

func TestPublicationRejectsPointSummaryMismatch(t *testing.T) {
	tests := []struct {
		name   string
		base   config.Config
		mutate fakeChildMutation
		want   string
	}{
		{
			name: "closed-loop concurrency",
			base: closedPublicationBase(8),
			mutate: func(_ *artifacts.RunMetadata, summary *aggregate.RunSummary) {
				summary.Load.ClosedLoop.RequestedConcurrency = 4
			},
			want: "requested concurrency",
		},
		{
			name: "open-loop request rate",
			base: openPublicationBase(100),
			mutate: func(_ *artifacts.RunMetadata, summary *aggregate.RunSummary) {
				summary.Load.OpenLoop.ConfiguredRequestRate = 50
			},
			want: "configured request rate",
		},
		{
			name: "token-length input target",
			base: tokenPublicationBase(512),
			mutate: func(_ *artifacts.RunMetadata, summary *aggregate.RunSummary) {
				summary.Workload.InputTargetTokens = intPointer(256)
			},
			want: "input target",
		},
		{
			name: "token-length resolved input",
			base: tokenPublicationBase(512),
			mutate: func(_ *artifacts.RunMetadata, summary *aggregate.RunSummary) {
				summary.Workload.InputResolvedTokens = intPointer(256)
			},
			want: "resolved input",
		},
		{
			name: "requested output",
			base: outputPublicationBase(128),
			mutate: func(_ *artifacts.RunMetadata, summary *aggregate.RunSummary) {
				summary.Workload.RequestedOutputMaxTokens = 64
			},
			want: "requested output maximum",
		},
		{
			name: "load mode",
			base: closedPublicationBase(8),
			mutate: func(_ *artifacts.RunMetadata, summary *aggregate.RunSummary) {
				summary.Load.Mode = benchmark.LoadModeOpenLoop
			},
			want: "load mode",
		},
		{
			name: "workload mode",
			base: tokenPublicationBase(512),
			mutate: func(_ *artifacts.RunMetadata, summary *aggregate.RunSummary) {
				summary.Workload.Mode = string(config.WorkloadModePrompt)
			},
			want: "workload mode",
		},
		{
			name: "prompt input evidence",
			base: closedPublicationBase(8),
			mutate: func(_ *artifacts.RunMetadata, summary *aggregate.RunSummary) {
				summary.Workload.InputTargetTokens = intPointer(1)
			},
			want: "token-length input evidence",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationFixture(t, test.base, test.mutate)
			fixture.reject(t, test.want)
		})
	}
}

func TestPublicationRejectsManifestFixedFieldMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{name: "model", mutate: func(manifest *Manifest) { manifest.Model = "other-model" }, want: "manifest model"},
		{name: "load mode", mutate: func(manifest *Manifest) { manifest.LoadMode = config.LoadModeOpenLoop }, want: "manifest load mode"},
		{name: "workload mode", mutate: func(manifest *Manifest) { manifest.WorkloadMode = config.WorkloadModeTokenLength }, want: "manifest workload mode"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationFixture(t, closedPublicationBase(8), nil)
			test.mutate(&fixture.manifest)
			fixture.reject(t, test.want)
		})
	}
}

func TestPublicationRejectsPointMetadataMismatch(t *testing.T) {
	tests := []struct {
		name   string
		base   config.Config
		mutate fakeChildMutation
		want   string
	}{
		{
			name: "load mode",
			base: closedPublicationBase(8),
			mutate: func(metadata *artifacts.RunMetadata, _ *aggregate.RunSummary) {
				metadata.Load.Mode = benchmark.LoadModeOpenLoop
			},
			want: "run.json load mode",
		},
		{
			name: "closed-loop concurrency",
			base: closedPublicationBase(8),
			mutate: func(metadata *artifacts.RunMetadata, _ *aggregate.RunSummary) {
				metadata.Load.ClosedLoop.RequestedConcurrency = 4
			},
			want: "run.json closed-loop configuration",
		},
		{
			name: "open-loop request rate",
			base: openPublicationBase(100),
			mutate: func(metadata *artifacts.RunMetadata, _ *aggregate.RunSummary) {
				metadata.Load.OpenLoop.RequestRate = 50
			},
			want: "run.json open-loop configuration",
		},
		{
			name: "workload mode",
			base: tokenPublicationBase(512),
			mutate: func(metadata *artifacts.RunMetadata, _ *aggregate.RunSummary) {
				metadata.Workload.Mode = string(config.WorkloadModePrompt)
			},
			want: "run.json workload mode",
		},
		{
			name: "requested output",
			base: outputPublicationBase(128),
			mutate: func(metadata *artifacts.RunMetadata, _ *aggregate.RunSummary) {
				metadata.Workload.Output.RequestedMaxTokens = 64
			},
			want: "run.json requested output maximum",
		},
		{
			name: "token-length input target",
			base: tokenPublicationBase(512),
			mutate: func(metadata *artifacts.RunMetadata, _ *aggregate.RunSummary) {
				metadata.Workload.Input.TargetTokens = 256
			},
			want: "run.json token-length input",
		},
		{
			name: "token-length resolved input",
			base: tokenPublicationBase(512),
			mutate: func(metadata *artifacts.RunMetadata, _ *aggregate.RunSummary) {
				metadata.Workload.Input.ResolvedTokens = 256
			},
			want: "run.json token-length input",
		},
		{
			name: "prompt input evidence",
			base: closedPublicationBase(8),
			mutate: func(metadata *artifacts.RunMetadata, _ *aggregate.RunSummary) {
				metadata.Workload.Input = &artifacts.WorkloadInputMetadata{TargetTokens: 1, ResolvedTokens: 1}
			},
			want: "run.json prompt workload",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationFixture(t, test.base, test.mutate)
			fixture.reject(t, test.want)
		})
	}
}

func TestPublicationRejectsContradictoryCombinedCSVFields(t *testing.T) {
	fixture := newPublicationFixture(t, closedPublicationBase(8), func(_ *artifacts.RunMetadata, summary *aggregate.RunSummary) {
		summary.Load.ClosedLoop.RequestedConcurrency = 4
	})
	encoded, err := MarshalCSV(fixture.manifest, fixture.summaries)
	if err != nil {
		t.Fatalf("MarshalCSV: %v", err)
	}
	records, err := csv.NewReader(bytes.NewReader(encoded)).ReadAll()
	if err != nil {
		t.Fatalf("read combined CSV: %v", err)
	}
	concurrencyColumn := csvColumnIndex(t, records[0], "concurrency")
	requestedConcurrencyColumn := csvColumnIndex(t, records[0], "requested_concurrency")
	if records[1][concurrencyColumn] != "8" || records[1][requestedConcurrencyColumn] != "4" {
		t.Fatalf("contradictory CSV evidence not constructed: %v", records[1])
	}
	fixture.reject(t, "requested concurrency")
}

func TestRunnerCleansStagingWhenPointSummaryContradictsPlan(t *testing.T) {
	base := closedPublicationBase(8)
	plan, err := BuildPlan(base)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	outputRoot := t.TempDir()
	runner := deterministicRunner(func(_ context.Context, request benchmarkexec.Request) (benchmarkexec.Result, error) {
		return writeFakeChildWithMutation(t, request, "run-contradiction", artifacts.RunStatusCompleted, func(_ *artifacts.RunMetadata, summary *aggregate.RunSummary) {
			summary.Load.ClosedLoop.RequestedConcurrency = 4
		}), nil
	})
	if _, err := runner.Run(context.Background(), RunRequest{Plan: plan, OutputRoot: outputRoot}); err == nil {
		t.Fatal("contradictory child summary unexpectedly published")
	}
	entries, err := os.ReadDir(filepath.Join(outputRoot, "experiments"))
	if err != nil {
		t.Fatalf("ReadDir experiments: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected runner publication left experiment entries: %v", entries)
	}
}

type publicationFixture struct {
	outputRoot  string
	publication *publication
	manifest    Manifest
	plan        Plan
	summaries   map[int]aggregate.RunSummary
}

func newPublicationFixture(t *testing.T, base config.Config, mutate fakeChildMutation) publicationFixture {
	t.Helper()
	plan, err := BuildPlan(base)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Points) != 1 {
		t.Fatalf("fixture plan has %d points, want 1", len(plan.Points))
	}
	outputRoot := t.TempDir()
	publication, err := beginPublication(outputRoot, "20260826T120000Z-exp-a31f00ff")
	if err != nil {
		t.Fatalf("beginPublication: %v", err)
	}
	t.Cleanup(publication.Abort)
	child := writeFakeChildWithMutation(t, benchmarkexec.Request{
		Config:       plan.Points[0].Config.Clone(),
		ArtifactRoot: publication.RunsRoot(),
	}, "run-one", artifacts.RunStatusCompleted, mutate)
	createdAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	manifest := newManifest(plan, "20260826T120000Z-exp-a31f00ff", createdAt)
	record := &manifest.Points[0]
	record.PointStatus = PointExecuted
	record.RunID = stringPointer(child.Metadata.RunID)
	record.RunPath = stringPointer(filepath.ToSlash(filepath.Join("runs", child.Metadata.RunID)))
	record.RunStatus = stringPointer(child.Metadata.RunStatus)
	manifest.Status = StatusCompleted
	manifest.Complete = true
	manifest.CompletedAt = createdAt.Add(time.Second)
	updateManifestCounts(&manifest)
	return publicationFixture{
		outputRoot:  outputRoot,
		publication: publication,
		manifest:    manifest,
		plan:        plan,
		summaries:   map[int]aggregate.RunSummary{1: child.Summary},
	}
}

func (fixture publicationFixture) reject(t *testing.T, want string) {
	t.Helper()
	if _, err := fixture.publication.Publish(fixture.manifest, fixture.plan, fixture.summaries); err == nil {
		t.Fatal("inconsistent experiment unexpectedly published")
	} else if !strings.Contains(err.Error(), want) {
		t.Fatalf("publication error = %q, want substring %q", err, want)
	}
	fixture.publication.Abort()
	entries, err := os.ReadDir(filepath.Join(fixture.outputRoot, "experiments"))
	if err != nil {
		t.Fatalf("ReadDir experiments: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected publication left experiment entries: %v", entries)
	}
}

func closedPublicationBase(concurrency int) config.Config {
	base := validBase()
	base.Experiment.ConcurrencyValues = []int{concurrency}
	return base
}

func openPublicationBase(requestRate float64) config.Config {
	base := validBase()
	base.Benchmark.Mode = config.LoadModeOpenLoop
	base.Benchmark.OpenLoop.RequestRate = requestRate
	base.Benchmark.OpenLoop.Duration = time.Second
	base.Benchmark.OpenLoop.MaxInFlight = 16
	base.Experiment.RequestRateValues = []float64{requestRate}
	return base
}

func tokenPublicationBase(inputTokens int) config.Config {
	base := validTokenBase()
	base.Experiment.InputTokenValues = []int{inputTokens}
	return base
}

func outputPublicationBase(outputTokens int) config.Config {
	base := validBase()
	base.Experiment.OutputTokenValues = []int{outputTokens}
	return base
}

func csvColumnIndex(t *testing.T, columns []string, name string) int {
	t.Helper()
	for index, column := range columns {
		if column == name {
			return index
		}
	}
	t.Fatalf("CSV column %q not found", name)
	return -1
}
