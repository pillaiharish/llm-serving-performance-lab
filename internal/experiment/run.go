package experiment

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmarkexec"
)

type ExecuteFunc func(context.Context, benchmarkexec.Request) (benchmarkexec.Result, error)

type Runner struct {
	Execute ExecuteFunc
	NewID   func() (string, error)
	Now     func() time.Time
}

type RunRequest struct {
	Plan              Plan
	APIKey            string
	OutputRoot        string
	SlentoreVersion   string
	ExperimentStarted func(string)
	PointCompleted    func(PointRecord, *benchmarkexec.Result)
}

func NewRunner() *Runner {
	return &Runner{Execute: benchmarkexec.Execute, NewID: NewID, Now: time.Now}
}

func (r *Runner) Run(ctx context.Context, request RunRequest) (Result, error) {
	if r == nil || r.Execute == nil || r.NewID == nil || r.Now == nil {
		return Result{}, fmt.Errorf("experiment runner dependencies are incomplete")
	}
	if len(request.Plan.Points) == 0 {
		return Result{}, fmt.Errorf("experiment plan has no points")
	}
	experimentID, err := r.NewID()
	if err != nil {
		return Result{}, err
	}
	publication, err := beginPublication(request.OutputRoot, experimentID)
	if err != nil {
		return Result{}, err
	}
	defer publication.Abort()
	if request.ExperimentStarted != nil {
		request.ExperimentStarted(experimentID)
	}

	manifest := newManifest(request.Plan, experimentID, r.Now().UTC())
	summaries := make(map[int]aggregate.RunSummary, len(request.Plan.Points))
	cancelled := false
	fatal := false
	for index, point := range request.Plan.Points {
		if ctx.Err() != nil {
			cancelled = true
			break
		}
		childResult, executeErr := r.Execute(ctx, benchmarkexec.Request{
			Config: point.Config.Clone(), APIKey: request.APIKey, ArtifactRoot: publication.RunsRoot(), SlentoreVersion: request.SlentoreVersion,
		})
		if executeErr != nil {
			record := &manifest.Points[index]
			if errors.Is(executeErr, context.Canceled) || ctx.Err() != nil {
				record.PointStatus = PointCancelled
				record.ErrorClass = ErrorClassCancellation
				record.Error = context.Canceled.Error()
				cancelled = true
				if request.PointCompleted != nil {
					request.PointCompleted(*record, nil)
				}
				break
			}
			if benchmarkexec.StageOf(executeErr) == benchmarkexec.FailurePreparation {
				record.PointStatus = PointPreflightFailed
				record.ErrorClass = ErrorClassPreparation
				record.Error = safePreparationError(executeErr)
				if request.PointCompleted != nil {
					request.PointCompleted(*record, nil)
				}
				continue
			}
			fatal = true
			manifest.ErrorClass = ErrorClassOrchestration
			manifest.Error = "child benchmark execution could not be completed"
			break
		}

		relativePath := filepath.ToSlash(filepath.Join("runs", childResult.Metadata.RunID))
		record := &manifest.Points[index]
		record.PointStatus = PointExecuted
		record.RunID = stringPointer(childResult.Metadata.RunID)
		record.RunPath = stringPointer(relativePath)
		record.RunStatus = stringPointer(childResult.Metadata.RunStatus)
		record.ErrorClass = childResult.Metadata.ErrorClass
		summaries[point.Index] = childResult.Summary
		if request.PointCompleted != nil {
			copy := childResult
			request.PointCompleted(*record, &copy)
		}
		if childResult.Metadata.RunStatus == artifacts.RunStatusCancelled || ctx.Err() != nil {
			cancelled = true
			break
		}
	}

	updateManifestCounts(&manifest)
	switch {
	case cancelled:
		manifest.Status = StatusCancelled
		manifest.Complete = false
	case fatal:
		manifest.Status = StatusFailed
		manifest.Complete = false
	default:
		manifest.Status = StatusCompleted
		manifest.Complete = manifest.NotStartedPoints == 0
	}
	manifest.CompletedAt = r.Now().UTC()
	artifactPath, err := publication.Publish(manifest, request.Plan, summaries)
	if err != nil {
		return Result{Manifest: manifest, Summaries: summaries}, err
	}
	return Result{Manifest: manifest, ArtifactPath: artifactPath, Summaries: summaries}, nil
}

func newManifest(plan Plan, experimentID string, createdAt time.Time) Manifest {
	points := make([]PointRecord, len(plan.Points))
	for index, point := range plan.Points {
		points[index] = PointRecord{PointIndex: point.Index, PointID: point.ID, Parameters: copyParameters(point.Parameters), PointStatus: PointNotStarted}
	}
	return Manifest{
		ExperimentSchemaVersion: SchemaVersion,
		ExperimentID:            experimentID,
		CreatedAt:               createdAt,
		Model:                   plan.Base.Endpoint.Model,
		LoadMode:                plan.Base.Benchmark.Mode,
		WorkloadMode:            plan.Base.Workload.Mode,
		Axes:                    copyAxes(plan.Axes),
		PlannedPoints:           len(points),
		Points:                  points,
	}
}

func updateManifestCounts(manifest *Manifest) {
	manifest.ExecutedPoints = 0
	manifest.PreflightFailedPoints = 0
	manifest.CancelledPoints = 0
	manifest.NotStartedPoints = 0
	manifest.ChildRunsCompleted = 0
	manifest.ChildRunsFailed = 0
	manifest.ChildRunsCancelled = 0
	for _, point := range manifest.Points {
		switch point.PointStatus {
		case PointExecuted:
			manifest.ExecutedPoints++
			if point.RunStatus != nil {
				switch *point.RunStatus {
				case artifacts.RunStatusCompleted:
					manifest.ChildRunsCompleted++
				case artifacts.RunStatusFailed:
					manifest.ChildRunsFailed++
				case artifacts.RunStatusCancelled:
					manifest.ChildRunsCancelled++
				}
			}
		case PointPreflightFailed:
			manifest.PreflightFailedPoints++
		case PointCancelled:
			manifest.CancelledPoints++
		case PointNotStarted:
			manifest.NotStartedPoints++
		}
	}
}

func safePreparationError(err error) string {
	if err == nil {
		return "workload preparation failed"
	}
	return err.Error()
}

func copyParameters(value Parameters) Parameters {
	return Parameters{Concurrency: copyIntPointer(value.Concurrency), RequestRate: copyFloatPointer(value.RequestRate), InputTokens: copyIntPointer(value.InputTokens), RequestedOutputTokens: value.RequestedOutputTokens}
}

func copyAxes(value Axes) Axes {
	return Axes{ConcurrencyValues: append([]int{}, value.ConcurrencyValues...), RequestRateValues: append([]float64{}, value.RequestRateValues...), InputTokenValues: append([]int{}, value.InputTokenValues...), OutputTokenValues: append([]int{}, value.OutputTokenValues...)}
}

func copyIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	return intPointer(*value)
}

func copyFloatPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return floatPointer(*value)
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}
