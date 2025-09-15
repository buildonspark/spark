package task

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	nameKey   = attribute.Key("task.name")
	resultKey = attribute.Key("task.result")
)

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
	if err != nil && errors.Is(err, errTaskPanic) {
		jobStatus = "panic"
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
