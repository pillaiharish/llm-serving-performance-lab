package benchmark

import (
	"context"
	"errors"
)

var ErrNoGeneratedContent = errors.New("stream completed without non-empty generated content")

// StreamExecutor performs one prepared streaming request while updating the
// supplied observation. Implementations must not persist artifacts or derive
// metrics in the measured path.
type StreamExecutor interface {
	Execute(context.Context, Request, *RequestObservation) error
}

type Runner struct {
	executor StreamExecutor
}

func NewRunner(executor StreamExecutor) *Runner {
	return &Runner{executor: executor}
}

// RunRequest owns exactly one request. Concurrency belongs to RunCoordinator,
// not this primitive.
func (r *Runner) RunRequest(ctx context.Context, request Request) Result {
	observation := RequestObservation{
		RunID:        request.RunID,
		RequestID:    request.RequestID,
		StreamEvents: make([]StreamEvent, 0),
		TokenTiming: TokenTimingEvidence{
			Source: TokenTimingSourceUnavailable,
		},
		Usage: TokenUsage{
			Source: TokenUsageSourceUnavailable,
		},
	}

	var err error
	if r == nil || r.executor == nil {
		err = errors.New("benchmark runner has no stream executor")
	} else {
		err = r.executor.Execute(ctx, request, &observation)
	}
	if err == nil && contentEventCount(observation.StreamEvents) == 0 {
		err = ErrNoGeneratedContent
	}
	if err != nil {
		observation.Error = err.Error()
	}

	return Result{Observation: observation, Err: err}
}

func contentEventCount(events []StreamEvent) int {
	count := 0
	for _, event := range events {
		if event.HasContent {
			count++
		}
	}
	return count
}
