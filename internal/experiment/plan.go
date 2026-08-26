package experiment

import (
	"fmt"
	"math"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmarkexec"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
)

const SchemaVersion = 1

type Axes struct {
	ConcurrencyValues []int     `json:"concurrency_values"`
	RequestRateValues []float64 `json:"request_rate_values"`
	InputTokenValues  []int     `json:"input_token_values"`
	OutputTokenValues []int     `json:"output_token_values"`
}

type Parameters struct {
	Concurrency           *int     `json:"concurrency"`
	RequestRate           *float64 `json:"request_rate"`
	InputTokens           *int     `json:"input_tokens"`
	RequestedOutputTokens int      `json:"requested_output_tokens"`
}

type Point struct {
	Index      int
	ID         string
	Parameters Parameters
	Config     config.Config
}

type Plan struct {
	Base      config.Config
	Axes      Axes
	MaxPoints int
	Points    []Point
}

func BuildPlan(base config.Config) (Plan, error) {
	axes := axesFromConfig(base)
	if len(axes.ConcurrencyValues)+len(axes.RequestRateValues)+len(axes.InputTokenValues)+len(axes.OutputTokenValues) == 0 {
		return Plan{}, fmt.Errorf("at least one sweep axis is required; use slentore bench for a single run")
	}
	if base.Experiment.Safety.MaxPoints <= 0 {
		return Plan{}, fmt.Errorf("experiment.safety.max_points must be greater than zero")
	}
	if err := validateApplicability(base, axes); err != nil {
		return Plan{}, err
	}
	if err := validateIntAxis("concurrency_values", axes.ConcurrencyValues); err != nil {
		return Plan{}, err
	}
	if err := validateRateAxis(axes.RequestRateValues); err != nil {
		return Plan{}, err
	}
	if err := validateIntAxis("input_token_values", axes.InputTokenValues); err != nil {
		return Plan{}, err
	}
	if err := validateIntAxis("output_token_values", axes.OutputTokenValues); err != nil {
		return Plan{}, err
	}

	inputCount := maxOne(len(axes.InputTokenValues))
	outputCount := maxOne(len(axes.OutputTokenValues))
	loadCount := 1
	if base.Benchmark.Mode == config.LoadModeClosedLoop {
		loadCount = maxOne(len(axes.ConcurrencyValues))
	} else {
		loadCount = maxOne(len(axes.RequestRateValues))
	}
	pointCount, err := checkedProduct(inputCount, outputCount, loadCount)
	if err != nil {
		return Plan{}, err
	}
	if pointCount > base.Experiment.Safety.MaxPoints {
		return Plan{}, fmt.Errorf("sweep has %d points, exceeding experiment.safety.max_points %d", pointCount, base.Experiment.Safety.MaxPoints)
	}

	inputValues := axes.InputTokenValues
	if len(inputValues) == 0 {
		inputValues = []int{base.Workload.InputTokens}
	}
	outputValues := axes.OutputTokenValues
	if len(outputValues) == 0 {
		outputValues = []int{base.Request.MaxOutputTokens}
	}

	points := make([]Point, 0, pointCount)
	for _, input := range inputValues {
		for _, output := range outputValues {
			if base.Benchmark.Mode == config.LoadModeClosedLoop {
				loads := axes.ConcurrencyValues
				if len(loads) == 0 {
					loads = []int{base.Benchmark.Concurrency}
				}
				for _, concurrency := range loads {
					point, pointErr := buildPoint(base, len(points)+1, &concurrency, nil, input, output)
					if pointErr != nil {
						return Plan{}, pointErr
					}
					points = append(points, point)
				}
				continue
			}
			loads := axes.RequestRateValues
			if len(loads) == 0 {
				loads = []float64{base.Benchmark.OpenLoop.RequestRate}
			}
			for _, rate := range loads {
				point, pointErr := buildPoint(base, len(points)+1, nil, &rate, input, output)
				if pointErr != nil {
					return Plan{}, pointErr
				}
				points = append(points, point)
			}
		}
	}
	return Plan{Base: base.Clone(), Axes: axes, MaxPoints: base.Experiment.Safety.MaxPoints, Points: points}, nil
}

func buildPoint(base config.Config, index int, concurrency *int, rate *float64, input, output int) (Point, error) {
	resolved := base.Clone()
	parameters := Parameters{RequestedOutputTokens: output}
	resolved.Request.MaxOutputTokens = output
	if resolved.Workload.Mode == config.WorkloadModeTokenLength {
		resolved.Workload.InputTokens = input
		parameters.InputTokens = intPointer(input)
	}
	if concurrency != nil {
		resolved.Benchmark.Concurrency = *concurrency
		parameters.Concurrency = intPointer(*concurrency)
	}
	if rate != nil {
		resolved.Benchmark.OpenLoop.RequestRate = *rate
		parameters.RequestRate = floatPointer(*rate)
	}
	if _, err := benchmarkexec.ValidateStatic(resolved); err != nil {
		return Point{}, fmt.Errorf("point-%06d is invalid: %w", index, err)
	}
	return Point{Index: index, ID: fmt.Sprintf("point-%06d", index), Parameters: parameters, Config: resolved}, nil
}

func axesFromConfig(value config.Config) Axes {
	return Axes{
		ConcurrencyValues: append([]int{}, value.Experiment.ConcurrencyValues...),
		RequestRateValues: append([]float64{}, value.Experiment.RequestRateValues...),
		InputTokenValues:  append([]int{}, value.Experiment.InputTokenValues...),
		OutputTokenValues: append([]int{}, value.Experiment.OutputTokenValues...),
	}
}

func validateApplicability(base config.Config, axes Axes) error {
	switch base.Benchmark.Mode {
	case config.LoadModeClosedLoop:
		if len(axes.RequestRateValues) != 0 {
			return fmt.Errorf("experiment.request_rate_values are forbidden for closed_loop load")
		}
	case config.LoadModeOpenLoop:
		if len(axes.ConcurrencyValues) != 0 {
			return fmt.Errorf("experiment.concurrency_values are forbidden for open_loop load")
		}
	default:
		return fmt.Errorf("benchmark.mode must be closed_loop or open_loop")
	}
	switch base.Workload.Mode {
	case config.WorkloadModePrompt:
		if len(axes.InputTokenValues) != 0 {
			return fmt.Errorf("experiment.input_token_values are forbidden for prompt workload")
		}
	case config.WorkloadModeTokenLength:
	default:
		return fmt.Errorf("workload.mode must be prompt or token_length")
	}
	return nil
}

func validateIntAxis(name string, values []int) error {
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		if value <= 0 {
			return fmt.Errorf("experiment.%s values must be greater than zero", name)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("experiment.%s contains duplicate value %d", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateRateAxis(values []float64) error {
	seen := make(map[float64]struct{}, len(values))
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return fmt.Errorf("experiment.request_rate_values must contain only finite values greater than zero")
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("experiment.request_rate_values contains duplicate value %g", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func checkedProduct(values ...int) (int, error) {
	result := 1
	for _, value := range values {
		if value <= 0 || result > int(^uint(0)>>1)/value {
			return 0, fmt.Errorf("sweep point count overflows int")
		}
		result *= value
	}
	return result, nil
}

func maxOne(value int) int {
	if value == 0 {
		return 1
	}
	return value
}

func intPointer(value int) *int {
	copy := value
	return &copy
}

func floatPointer(value float64) *float64 {
	copy := value
	return &copy
}
