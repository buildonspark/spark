package task

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

const (
	nameKey   = attribute.Key("task.name")
	resultKey = attribute.Key("task.result")
)

// ephemeralDivergenceCounter counts tasks where the ephemeral transaction committed
// successfully but the subsequent main transaction commit failed. This divergence
// means the ephemeral DB has durable state the main DB does not, and persists until
// a successful retry reconciles the two.
var ephemeralDivergenceCounter = sync.OnceValue(func() metric.Int64Counter {
	c, err := otel.Meter("gocron").Int64Counter(
		"gocron.task_ephemeral_divergence_total",
		metric.WithDescription("Number of tasks where the ephemeral transaction committed but the main transaction commit failed, creating a DB divergence"),
	)
	if err != nil {
		otel.Handle(err)
	}
	if c == nil {
		return noop.Int64Counter{}
	}
	return c
})

type Monitor struct {
	taskCount    metric.Int64Counter
	taskDuration metric.Float64Histogram
}

func NewMonitor() (*Monitor, error) {
	meter := otel.Meter("gocron")

	jobCount, err := meter.Int64Counter(
		"gocron.task_count_total",
		metric.WithDescription("Total number of tasks executed"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create task count metric: %w", err)
	}

	jobDuration, err := meter.Float64Histogram(
		"gocron.task_duration_milliseconds",
		metric.WithDescription("Duration of tasks in milliseconds."),
		metric.WithUnit("ms"),
		metric.WithExplicitBucketBoundaries(
			// Replace the buckets at the lower end (e.g. 5, 10, 25, 50, 75ms) with buckets up to 60s, to
			// capture the longer task durations.
			100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000, 15000, 30000, 45000, 60000,
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create task duration metric: %w", err)
	}

	return &Monitor{
		taskCount:    jobCount,
		taskDuration: jobDuration,
	}, nil
}

func (t *Monitor) IncrementJob(_ uuid.UUID, name string, _ []string, status gocron.JobStatus) {}

func (t *Monitor) RecordJobTiming(startTime, endTime time.Time, _ uuid.UUID, name string, _ []string) {
}

func (t *Monitor) RecordJobTimingWithStatus(startTime, endTime time.Time, id uuid.UUID, name string, tags []string, status gocron.JobStatus, err error) {
	jobStatus := string(status)
	if err != nil {
		switch {
		case errors.Is(err, errTaskPanic):
			jobStatus = "panic"
		case errors.Is(err, errTaskTimeout):
			jobStatus = "timeout"
		case errors.Is(err, errTaskDisabled):
			jobStatus = "disabled"
		}
	}

	t.taskCount.Add(
		context.Background(),
		1,
		metric.WithAttributes(
			nameKey.String(name),
			resultKey.String(jobStatus),
		),
	)

	duration := endTime.Sub(startTime).Milliseconds()
	t.taskDuration.Record(
		context.Background(),
		float64(duration),
		metric.WithAttributes(
			nameKey.String(name),
		),
	)
}
