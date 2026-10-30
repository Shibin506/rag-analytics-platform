// Package core holds the ingest logic shared by the gRPC and REST transports:
// validate each event's envelope and payload, then hand valid ones to the sink.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/schema"
)

// Event is one incoming event before validation.
type Event struct {
	EventID    string          `json:"event_id"`
	EventType  string          `json:"event_type"`
	OccurredAt string          `json:"occurred_at"`
	UserID     string          `json:"user_id"`
	Properties json.RawMessage `json:"properties"`
}

type Rejection struct {
	Index   int    `json:"index"`
	EventID string `json:"event_id,omitempty"`
	Reason  string `json:"reason"`
}

type Result struct {
	Accepted int         `json:"accepted"`
	Rejected []Rejection `json:"rejected"`
}

// rawRecord is the line written to the lake. Field names match the Spark
// job's schema (pipelines/spark/compact_events.py).
type rawRecord struct {
	EventID       string          `json:"event_id"`
	TenantID      string          `json:"tenant_id"`
	EventType     string          `json:"event_type"`
	SchemaVersion int             `json:"schema_version"`
	OccurredAt    string          `json:"occurred_at"`
	IngestedAt    string          `json:"ingested_at"`
	UserID        string          `json:"user_id"`
	Properties    json.RawMessage `json:"properties"`
}

type Sink interface {
	Add(ctx context.Context, tenant string, day time.Time, line []byte) error
}

type Recorder interface {
	Record(tenant, eventType string, accepted bool)
}

type Validator interface {
	Validate(tenant, eventType string, payload any) (int, error)
}

var ErrBatchTooLarge = errors.New("batch too large")

type Service struct {
	Validator Validator
	Sink      Sink
	Stats     Recorder
	MaxBatch  int
	// Events may be at most this far in the future (client clock skew) ...
	MaxFuture time.Duration
	// ... and at most this old (backfills of historical data are allowed).
	MaxAge time.Duration
	Now    func() time.Time
}

func New(v Validator, s Sink, r Recorder, maxBatch int) *Service {
	return &Service{
		Validator: v, Sink: s, Stats: r, MaxBatch: maxBatch,
		MaxFuture: 5 * time.Minute, MaxAge: 400 * 24 * time.Hour, Now: time.Now,
	}
}

// Ingest validates every event independently: one bad event never blocks the
// rest of the batch. Errors from the sink (S3) fail the whole call so the
// client retries; event IDs make those retries safe to de-duplicate later.
func (s *Service) Ingest(ctx context.Context, tenant string, events []Event) (Result, error) {
	if len(events) > s.MaxBatch {
		return Result{}, fmt.Errorf("%w: %d events (max %d)", ErrBatchTooLarge, len(events), s.MaxBatch)
	}
	res := Result{Rejected: []Rejection{}}
	now := s.Now().UTC()
	ingestedAt := now.Format(time.RFC3339Nano)

	for i, e := range events {
		rec, day, err := s.check(tenant, e, now)
		if err != nil {
			res.Rejected = append(res.Rejected, Rejection{Index: i, EventID: e.EventID, Reason: err.Error()})
			s.Stats.Record(tenant, e.EventType, false)
			continue
		}
		rec.IngestedAt = ingestedAt
		line, err := json.Marshal(rec)
		if err != nil {
			return res, err
		}
		if err := s.Sink.Add(ctx, tenant, day, line); err != nil {
			return res, fmt.Errorf("storing events: %w", err)
		}
		res.Accepted++
		s.Stats.Record(tenant, e.EventType, true)
	}
	return res, nil
}

func (s *Service) check(tenant string, e Event, now time.Time) (rawRecord, time.Time, error) {
	var zero rawRecord
	id, err := uuid.Parse(e.EventID)
	if err != nil {
		return zero, time.Time{}, errors.New("event_id must be a UUID")
	}
	if e.EventType == "" {
		return zero, time.Time{}, errors.New("event_type is required")
	}
	at, err := time.Parse(time.RFC3339Nano, e.OccurredAt)
	if err != nil {
		return zero, time.Time{}, errors.New("occurred_at must be an RFC 3339 timestamp")
	}
	at = at.UTC()
	if at.After(now.Add(s.MaxFuture)) {
		return zero, time.Time{}, errors.New("occurred_at is in the future")
	}
	if at.Before(now.Add(-s.MaxAge)) {
		return zero, time.Time{}, errors.New("occurred_at is too old")
	}
	props := bytes.TrimSpace(e.Properties)
	if len(props) == 0 {
		props = []byte("{}")
	}
	// jsonschema.UnmarshalJSON keeps numbers exact (json.Number), so integer
	// checks are not fooled by float64 rounding.
	payload, err := jsonschema.UnmarshalJSON(bytes.NewReader(props))
	if err != nil {
		return zero, time.Time{}, errors.New("properties must be valid JSON")
	}
	if _, ok := payload.(map[string]any); !ok {
		return zero, time.Time{}, errors.New("properties must be a JSON object")
	}
	version, err := s.Validator.Validate(tenant, e.EventType, payload)
	if err != nil {
		return zero, time.Time{}, err
	}
	return rawRecord{
		EventID:       id.String(),
		TenantID:      tenant,
		EventType:     e.EventType,
		SchemaVersion: version,
		OccurredAt:    at.Format(time.RFC3339Nano),
		UserID:        e.UserID,
		Properties:    json.RawMessage(props),
	}, at, nil
}

// compile-time check that the real registry satisfies Validator.
var _ Validator = (*schema.Registry)(nil)
