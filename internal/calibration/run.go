package calibration

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmarkexec"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/experiment"
)

type ExecuteFunc func(context.Context, benchmarkexec.Request) (benchmarkexec.Result, error)

type Runner struct {
	Execute        ExecuteFunc
	NewID          func() (string, error)
	Now            func() time.Time
	SampleInterval time.Duration
}

type RunRequest struct {
	Plan               experiment.Plan
	APIKey             string
	OutputRoot         string
	SlentoreVersion    string
	MaxSchedulerLagP95 *time.Duration
	CalibrationStarted func(string)
	ExperimentStarted  func(string)
	PointCompleted     func(Point)
}

func NewRunner() *Runner {
	return &Runner{Execute: benchmarkexec.Execute, NewID: NewID, Now: time.Now, SampleInterval: DefaultSampleInterval}
}

func ValidatePlan(plan experiment.Plan, maxSchedulerLagP95 *time.Duration) error {
	if len(plan.Points) == 0 {
		return fmt.Errorf("calibration plan has no points")
	}
	if plan.Base.Workload.Mode != config.WorkloadModePrompt {
		return fmt.Errorf("client calibration requires prompt workload mode")
	}
	if len(plan.Axes.InputTokenValues) != 0 || len(plan.Axes.OutputTokenValues) != 0 {
		return fmt.Errorf("client calibration does not support token-shape axes")
	}
	switch plan.Base.Benchmark.Mode {
	case config.LoadModeClosedLoop:
		if len(plan.Axes.ConcurrencyValues) == 0 || len(plan.Axes.RequestRateValues) != 0 {
			return fmt.Errorf("closed-loop calibration requires concurrency_values only")
		}
		if maxSchedulerLagP95 != nil {
			return fmt.Errorf("scheduler-lag budget applies only to open-loop calibration")
		}
	case config.LoadModeOpenLoop:
		if len(plan.Axes.RequestRateValues) == 0 || len(plan.Axes.ConcurrencyValues) != 0 {
			return fmt.Errorf("open-loop calibration requires request_rate_values only")
		}
	default:
		return fmt.Errorf("unsupported calibration load mode %q", plan.Base.Benchmark.Mode)
	}
	if maxSchedulerLagP95 != nil && *maxSchedulerLagP95 <= 0 {
		return fmt.Errorf("scheduler-lag budget must be greater than zero")
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, request RunRequest) (Result, error) {
	if r == nil || r.Execute == nil || r.NewID == nil || r.Now == nil {
		return Result{}, fmt.Errorf("calibration runner dependencies are incomplete")
	}
	if err := ValidatePlan(request.Plan, request.MaxSchedulerLagP95); err != nil {
		return Result{}, err
	}
	calibrationID, err := r.NewID()
	if err != nil {
		return Result{}, err
	}
	createdAt := r.Now().UTC()
	if request.CalibrationStarted != nil {
		request.CalibrationStarted(calibrationID)
	}

	resources := make(map[int]ResourceEvidence, len(request.Plan.Points))
	children := make(map[int]benchmarkexec.Result, len(request.Plan.Points))
	nextPoint := 0
	experimentRunner := experiment.NewRunner()
	experimentRunner.Execute = func(executeContext context.Context, executeRequest benchmarkexec.Request) (benchmarkexec.Result, error) {
		nextPoint++
		pointIndex := nextPoint
		var child benchmarkexec.Result
		var executeErr error
		sampler := NewSampler()
		sampler.Interval = r.SampleInterval
		resource := sampler.Measure(func() {
			child, executeErr = r.Execute(executeContext, executeRequest)
		})
		resources[pointIndex] = resource
		if executeErr == nil {
			children[pointIndex] = child
		}
		return child, executeErr
	}

	experimentResult, err := experimentRunner.Run(ctx, experiment.RunRequest{
		Plan: request.Plan, APIKey: request.APIKey, OutputRoot: request.OutputRoot, SlentoreVersion: request.SlentoreVersion,
		ExperimentStarted: request.ExperimentStarted,
	})
	if err != nil {
		return Result{Experiment: experimentResult, ExperimentPath: experimentResult.ArtifactPath}, err
	}

	lagBudgetMS := durationMilliseconds(request.MaxSchedulerLagP95)
	manifest := Manifest{
		CalibrationSchemaVersion: SchemaVersion,
		CalibrationID:            calibrationID,
		CreatedAt:                createdAt,
		CompletedAt:              r.Now().UTC(),
		Status:                   experimentResult.Manifest.Status,
		Complete:                 experimentResult.Manifest.Complete,
		LoadMode:                 request.Plan.Base.Benchmark.Mode,
		ConcurrencyValues:        append([]int(nil), request.Plan.Axes.ConcurrencyValues...),
		RequestRateValues:        append([]float64(nil), request.Plan.Axes.RequestRateValues...),
		MaxSchedulerLagP95MS:     lagBudgetMS,
		ExperimentID:             experimentResult.Manifest.ExperimentID,
		ExperimentPath:           filepath.ToSlash(filepath.Join("experiments", experimentResult.Manifest.ExperimentID)),
		PlannedPoints:            len(experimentResult.Manifest.Points),
		Points:                   make([]Point, 0, len(experimentResult.Manifest.Points)),
	}

	for _, record := range experimentResult.Manifest.Points {
		wrapped := experimentPoint{
			index: record.PointIndex, id: record.PointID, status: record.PointStatus,
			concurrency: record.Parameters.Concurrency, requestRate: record.Parameters.RequestRate,
			runID: record.RunID, runPath: record.RunPath, runStatus: record.RunStatus,
		}
		resource, hasResource := resources[record.PointIndex]
		child, hasChild := children[record.PointIndex]
		var point Point
		if hasChild {
			point = pointFromResult(wrapped, child, resource, manifest.ExperimentID, lagBudgetMS)
			manifest.EvaluatedPoints++
			if *point.DeliveryClean {
				manifest.CleanPoints++
			} else {
				manifest.DirtyPoints++
			}
			if manifest.ClientDiagnostics == nil {
				diagnostics := child.Metadata.ClientDiagnostics
				manifest.ClientDiagnostics = &diagnostics
			}
		} else {
			point = Point{
				PointIndex: record.PointIndex, PointID: record.PointID, PointStatus: record.PointStatus,
				RequestedConcurrency: copyInt(record.Parameters.Concurrency), ConfiguredRequestRate: copyFloat(record.Parameters.RequestRate),
				ChildRunID: copyString(record.RunID), ChildRunPath: calibrationChildPath(manifest.ExperimentID, record.RunPath), RunStatus: copyString(record.RunStatus),
				DeliveryReasons: []string{},
			}
			if hasResource {
				point.Resource = &resource
			}
			manifest.UnevaluatedPoints++
		}
		manifest.Points = append(manifest.Points, point)
		if request.PointCompleted != nil {
			request.PointCompleted(point)
		}
	}

	artifactPath, err := NewWriter(request.OutputRoot).Write(manifest)
	result := Result{Manifest: manifest, ArtifactPath: artifactPath, Experiment: experimentResult, ExperimentPath: experimentResult.ArtifactPath}
	if err != nil {
		return result, err
	}
	return result, nil
}

type experimentPoint struct {
	index       int
	id          string
	status      string
	concurrency *int
	requestRate *float64
	runID       *string
	runPath     *string
	runStatus   *string
}

func durationMilliseconds(value *time.Duration) *float64 {
	if value == nil {
		return nil
	}
	milliseconds := float64(*value) / float64(time.Millisecond)
	return &milliseconds
}

func calibrationChildPath(experimentID string, runPath *string) *string {
	if runPath == nil {
		return nil
	}
	value := filepath.ToSlash(filepath.Join("experiments", experimentID, filepath.FromSlash(*runPath)))
	return &value
}

func boolPointer(value bool) *bool { return &value }

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
