// Package sink buffers accepted events and writes them to the lake's raw zone
// as newline-delimited JSON, one object per flush, laid out in Hive partitions:
//
//	raw/events/tenant=<tenant>/dt=<YYYY-MM-DD>/<uuidv7>.json
//
// The partition comes from the event's own time, so late events still land in
// the day they happened and the daily Spark job sees them.
package sink

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/objstore"
)

type partition struct {
	tenant string
	day    string
}

type buffer struct {
	buf     bytes.Buffer
	rows    int
	started time.Time
}

type Batcher struct {
	store     objstore.Store
	prefix    string
	maxBytes  int
	maxAge    time.Duration
	log       *slog.Logger
	now       func() time.Time
	onFlushed func(rows int, bytes int)

	mu   sync.Mutex
	bufs map[partition]*buffer
	// flushMu serialises flushes so a ticker flush and a size-triggered flush
	// never upload the same rows twice.
	flushMu sync.Mutex
}

type Options struct {
	Prefix    string
	MaxBytes  int
	MaxAge    time.Duration
	Log       *slog.Logger
	OnFlushed func(rows int, bytes int)
}

func NewBatcher(store objstore.Store, o Options) *Batcher {
	if o.Prefix == "" {
		o.Prefix = "raw/events"
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Batcher{
		store: store, prefix: o.Prefix, maxBytes: o.MaxBytes, maxAge: o.MaxAge,
		log: o.Log, now: time.Now, onFlushed: o.OnFlushed, bufs: map[partition]*buffer{},
	}
}

// Add appends one JSON line for a tenant/day. If that buffer is now over the
// size limit it is flushed before Add returns, which applies back-pressure to
// very large clients instead of growing memory without bound.
func (b *Batcher) Add(ctx context.Context, tenant string, day time.Time, line []byte) error {
	p := partition{tenant, day.UTC().Format("2006-01-02")}
	b.mu.Lock()
	buf, ok := b.bufs[p]
	if !ok {
		buf = &buffer{started: b.now()}
		b.bufs[p] = buf
	}
	buf.buf.Write(line)
	buf.buf.WriteByte('\n')
	buf.rows++
	full := buf.buf.Len() >= b.maxBytes
	b.mu.Unlock()

	if full {
		return b.flush(ctx, func(q partition, _ *buffer) bool { return q == p })
	}
	return nil
}

// FlushDue writes every buffer older than MaxAge.
func (b *Batcher) FlushDue(ctx context.Context) error {
	cutoff := b.now().Add(-b.maxAge)
	return b.flush(ctx, func(_ partition, buf *buffer) bool { return !buf.started.After(cutoff) })
}

// FlushAll writes everything (used on shutdown).
func (b *Batcher) FlushAll(ctx context.Context) error {
	return b.flush(ctx, func(partition, *buffer) bool { return true })
}

// Run flushes due buffers every tick until ctx ends, then flushes the rest.
func (b *Batcher) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := b.FlushAll(shutdown); err != nil {
				b.log.Error("final flush failed", "err", err)
			}
			cancel()
			return
		case <-t.C:
			if err := b.FlushDue(ctx); err != nil {
				b.log.Error("flush failed", "err", err)
			}
		}
	}
}

func (b *Batcher) flush(ctx context.Context, pick func(partition, *buffer) bool) error {
	b.flushMu.Lock()
	defer b.flushMu.Unlock()

	b.mu.Lock()
	taken := map[partition]*buffer{}
	for p, buf := range b.bufs {
		if pick(p, buf) {
			taken[p] = buf
			delete(b.bufs, p)
		}
	}
	b.mu.Unlock()

	var firstErr error
	for p, buf := range taken {
		key := fmt.Sprintf("%s/tenant=%s/dt=%s/%s.json", b.prefix, p.tenant, p.day, uuid.Must(uuid.NewV7()))
		body := buf.buf.Bytes()
		if err := b.store.Put(ctx, key, body, "application/x-ndjson"); err != nil {
			// Put the rows back so the next flush retries them; nothing is lost.
			b.requeue(p, buf)
			if firstErr == nil {
				firstErr = fmt.Errorf("writing %s: %w", key, err)
			}
			continue
		}
		b.log.Debug("flushed", "key", key, "rows", buf.rows, "bytes", len(body))
		if b.onFlushed != nil {
			b.onFlushed(buf.rows, len(body))
		}
	}
	return firstErr
}

func (b *Batcher) requeue(p partition, old *buffer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.bufs[p]; ok {
		old.buf.Write(cur.buf.Bytes())
		old.rows += cur.rows
	}
	b.bufs[p] = old
}

// Pending reports buffered rows (for tests and metrics).
func (b *Batcher) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, buf := range b.bufs {
		n += buf.rows
	}
	return n
}
