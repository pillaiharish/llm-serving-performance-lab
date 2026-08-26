package experiment

import (
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
)

const (
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
	StatusFailed    = "failed"

	PointExecuted        = "executed"
	PointPreflightFailed = "preflight_failed"
	PointCancelled       = "cancelled"
	PointNotStarted      = "not_started"

	ErrorClassPreparation   = "workload_preparation_error"
	ErrorClassCancellation  = "parent_cancelled"
	ErrorClassOrchestration = "orchestration_error"
)

type PointRecord struct {
	PointIndex  int        `json:"point_index"`
	PointID     string     `json:"point_id"`
	Parameters  Parameters `json:"parameters"`
	PointStatus string     `json:"point_status"`
	RunID       *string    `json:"run_id"`
	RunPath     *string    `json:"run_path"`
	RunStatus   *string    `json:"run_status"`
	ErrorClass  string     `json:"error_class,omitempty"`
	Error       string     `json:"error,omitempty"`
}

type Manifest struct {
	ExperimentSchemaVersion int                 `json:"experiment_schema_version"`
	ExperimentID            string              `json:"experiment_id"`
	CreatedAt               time.Time           `json:"created_at"`
	CompletedAt             time.Time           `json:"completed_at"`
	Status                  string              `json:"status"`
	Complete                bool                `json:"complete"`
	Model                   string              `json:"model"`
	LoadMode                config.LoadMode     `json:"load_mode"`
	WorkloadMode            config.WorkloadMode `json:"workload_mode"`
	Axes                    Axes                `json:"axes"`
	PlannedPoints           int                 `json:"planned_points"`
	ExecutedPoints          int                 `json:"executed_points"`
	PreflightFailedPoints   int                 `json:"preflight_failed_points"`
	CancelledPoints         int                 `json:"cancelled_points"`
	NotStartedPoints        int                 `json:"not_started_points"`
	ChildRunsCompleted      int                 `json:"child_runs_completed"`
	ChildRunsFailed         int                 `json:"child_runs_failed"`
	ChildRunsCancelled      int                 `json:"child_runs_cancelled"`
	ErrorClass              string              `json:"error_class,omitempty"`
	Error                   string              `json:"error,omitempty"`
	Points                  []PointRecord       `json:"points"`
}

type Result struct {
	Manifest     Manifest
	ArtifactPath string
	Summaries    map[int]aggregate.RunSummary
}

func (r Result) Successful() bool {
	if r.Manifest.Status != StatusCompleted || !r.Manifest.Complete || r.Manifest.PreflightFailedPoints != 0 || r.Manifest.CancelledPoints != 0 || r.Manifest.NotStartedPoints != 0 {
		return false
	}
	return r.Manifest.ExecutedPoints == r.Manifest.PlannedPoints && r.Manifest.ChildRunsCompleted == r.Manifest.PlannedPoints
}
