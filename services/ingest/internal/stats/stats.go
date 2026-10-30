// Package stats records ingest telemetry two ways: Prometheus metrics for
// operators, and per-minute rows in Postgres that the catalog's GraphQL API
// serves to client dashboards.
package stats

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	EventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "lumen_ingest_events_total",
		Help: "Events received, by tenant and outcome.",
	}, []string{"tenant", "outcome"})

	RequestSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "lumen_ingest_request_seconds",
		Help:    "Ingest request latency by transport and method.",
		Buckets: prometheus.ExponentialBuckets(0.0005, 2, 14),
	}, []string{"transport", "method"})

	FlushedBytes = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "lumen_ingest_flushed_bytes_total",
		Help: "Bytes written to the raw zone in S3.",
	})

	FlushedObjects = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "lumen_ingest_flushed_objects_total",
		Help: "Objects written to the raw zone in S3.",
	})
)

func init() {
	prometheus.MustRegister(EventsTotal, RequestSeconds, FlushedBytes, FlushedObjects)
}

type rowKey struct {
	tenant    string
	minute    time.Time
	eventType string
}

type counts struct{ accepted, rejected int64 }

// Minutely aggregates accepted/rejected counts per tenant, minute and event
// type in memory, then upserts them in one batch.
type Minutely struct {
	mu   sync.Mutex
	rows map[rowKey]*counts
	now  func() time.Time
}

func NewMinutely() *Minutely {
	return &Minutely{rows: map[rowKey]*counts{}, now: time.Now}
}

func (m *Minutely) Record(tenant, eventType string, accepted bool) {
	outcome := "accepted"
	if !accepted {
		outcome = "rejected"
	}
	EventsTotal.WithLabelValues(tenant, outcome).Inc()

	if eventType == "" {
		eventType = "(unknown)"
	}
	k := rowKey{tenant, m.now().UTC().Truncate(time.Minute), eventType}
	m.mu.Lock()
	c, ok := m.rows[k]
	if !ok {
		c = &counts{}
		m.rows[k] = c
	}
	if accepted {
		c.accepted++
	} else {
		c.rejected++
	}
	m.mu.Unlock()
}

func (m *Minutely) drain() map[rowKey]*counts {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.rows
	m.rows = map[rowKey]*counts{}
	return out
}

// Flush upserts and resets the counters. On failure the counts are merged back.
func (m *Minutely) Flush(ctx context.Context, pool *pgxpool.Pool) error {
	rows := m.drain()
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for k, c := range rows {
		batch.Queue(`
			INSERT INTO ingest_stats (tenant_id, minute, event_type, accepted, rejected)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, minute, event_type) DO UPDATE
			SET accepted = ingest_stats.accepted + EXCLUDED.accepted,
			    rejected = ingest_stats.rejected + EXCLUDED.rejected`,
			k.tenant, k.minute, k.eventType, c.accepted, c.rejected)
	}
	if err := pool.SendBatch(ctx, batch).Close(); err != nil {
		m.mu.Lock()
		for k, c := range rows {
			if cur, ok := m.rows[k]; ok {
				cur.accepted += c.accepted
				cur.rejected += c.rejected
			} else {
				m.rows[k] = c
			}
		}
		m.mu.Unlock()
		return err
	}
	return nil
}
