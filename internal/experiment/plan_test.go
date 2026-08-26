package experiment

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
)

func TestBuildPlanPreservesUserOrder(t *testing.T) {
	base := validBase()
	base.Experiment.ConcurrencyValues = []int{8, 1, 4}
	plan, err := BuildPlan(base)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for index, want := range []int{8, 1, 4} {
		point := plan.Points[index]
		if point.Index != index+1 || point.ID != fmt.Sprintf("point-%06d", index+1) || point.Parameters.Concurrency == nil || *point.Parameters.Concurrency != want {
			t.Fatalf("point %d = %+v", index, point)
		}
	}
}

func TestBuildPlanUsesDocumentedCartesianOrder(t *testing.T) {
	base := validTokenBase()
	base.Experiment.InputTokenValues = []int{128, 512}
	base.Experiment.OutputTokenValues = []int{64, 128}
	base.Experiment.ConcurrencyValues = []int{1, 4}
	plan, err := BuildPlan(base)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	want := [][3]int{{128, 64, 1}, {128, 64, 4}, {128, 128, 1}, {128, 128, 4}, {512, 64, 1}, {512, 64, 4}, {512, 128, 1}, {512, 128, 4}}
	if len(plan.Points) != len(want) {
		t.Fatalf("points = %d", len(plan.Points))
	}
	for index, values := range want {
		point := plan.Points[index]
		if *point.Parameters.InputTokens != values[0] || point.Parameters.RequestedOutputTokens != values[1] || *point.Parameters.Concurrency != values[2] {
			t.Fatalf("point %d = %+v, want %v", index+1, point.Parameters, values)
		}
	}
}

func TestBuildPlanRejectsInvalidAxesAndSafety(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*config.Config)
		want  string
	}{
		{name: "no axes", alter: func(*config.Config) {}, want: "at least one"},
		{name: "duplicate integer", alter: func(c *config.Config) { c.Experiment.ConcurrencyValues = []int{1, 2, 2} }, want: "duplicate"},
		{name: "duplicate rate", alter: func(c *config.Config) {
			c.Benchmark.Mode = config.LoadModeOpenLoop
			c.Benchmark.OpenLoop.RequestRate = 1
			c.Benchmark.OpenLoop.Duration = time.Second
			c.Benchmark.OpenLoop.MaxInFlight = 2
			c.Experiment.RequestRateValues = []float64{1.5, 1.5}
		}, want: "duplicate"},
		{name: "wrong load axis", alter: func(c *config.Config) { c.Experiment.RequestRateValues = []float64{1} }, want: "forbidden"},
		{name: "wrong workload axis", alter: func(c *config.Config) { c.Experiment.InputTokenValues = []int{1} }, want: "forbidden"},
		{name: "max points", alter: func(c *config.Config) {
			c.Experiment.ConcurrencyValues = []int{1, 2, 3}
			c.Experiment.OutputTokenValues = []int{1, 2, 3}
			c.Experiment.Safety.MaxPoints = 8
		}, want: "9 points"},
		{name: "unsafe point", alter: func(c *config.Config) { c.Experiment.ConcurrencyValues = []int{257} }, want: "max_concurrency"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := validBase()
			test.alter(&base)
			_, err := BuildPlan(base)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBuildPlanAllowsSweptReplacementAndKeepsConfigsIndependent(t *testing.T) {
	base := validBase()
	base.Request.MaxOutputTokens = 0
	base.Experiment.OutputTokenValues = []int{16, 32}
	threshold := time.Second
	base.Benchmark.SLO.TTFT = &threshold
	plan, err := BuildPlan(base)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	plan.Points[0].Config.Request.MaxOutputTokens = 99
	*plan.Points[0].Config.Benchmark.SLO.TTFT = 2 * time.Second
	if plan.Points[1].Config.Request.MaxOutputTokens != 32 || *plan.Points[1].Config.Benchmark.SLO.TTFT != time.Second || base.Request.MaxOutputTokens != 0 || *base.Benchmark.SLO.TTFT != time.Second {
		t.Fatal("point configurations or base configuration are aliased")
	}
}

func TestCheckedProductRejectsOverflow(t *testing.T) {
	max := int(^uint(0) >> 1)
	if _, err := checkedProduct(max, 2); err == nil {
		t.Fatal("overflowing product unexpectedly accepted")
	}
}

func validBase() config.Config {
	base := config.Default()
	base.Endpoint.BaseURL = "http://127.0.0.1:8000/v1"
	base.Endpoint.Model = "model"
	base.Request.Prompt = "private prompt"
	return base
}

func validTokenBase() config.Config {
	base := validBase()
	base.Request.Prompt = ""
	base.Workload.Mode = config.WorkloadModeTokenLength
	base.Workload.InputTokens = 128
	base.Workload.Tokenizer.Adapter = config.TokenizerAdapterVLLM
	base.Workload.Tokenizer.URL = "http://127.0.0.1:8000/tokenize"
	return base
}
