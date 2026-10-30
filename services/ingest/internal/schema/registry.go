// Package schema keeps compiled JSON Schemas for every tenant's event types and
// validates event payloads against the latest version.
package schema

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Key identifies one event type of one tenant.
type Key struct {
	Tenant    string
	EventType string
}

// Entry is the latest compiled schema of an event type.
type Entry struct {
	Version int
	schema  *jsonschema.Schema
}

// Registry is safe for concurrent use. Schemas are swapped in atomically on
// each refresh, so validation never sees a half-loaded set.
type Registry struct {
	mu      sync.RWMutex
	entries map[Key]Entry
}

func NewRegistry() *Registry {
	return &Registry{entries: map[Key]Entry{}}
}

// ErrUnknownEventType is returned for event types with no registered schema.
type ErrUnknownEventType struct{ EventType string }

func (e ErrUnknownEventType) Error() string {
	return fmt.Sprintf("event type %q is not registered", e.EventType)
}

// Validate checks a decoded JSON payload and returns the schema version used.
func (r *Registry) Validate(tenant, eventType string, payload any) (int, error) {
	r.mu.RLock()
	e, ok := r.entries[Key{tenant, eventType}]
	r.mu.RUnlock()
	if !ok {
		return 0, ErrUnknownEventType{eventType}
	}
	if err := e.schema.Validate(payload); err != nil {
		return e.Version, fmt.Errorf("schema v%d: %s", e.Version, flatten(err))
	}
	return e.Version, nil
}

// Source is one schema row as stored in the registry table.
type Source struct {
	Key
	Version int
	Schema  json.RawMessage
}

// Load compiles sources and replaces the registry's contents. Only the highest
// version per key is kept. A schema that fails to compile is skipped and
// reported, so one bad row cannot take ingestion down.
func (r *Registry) Load(sources []Source) []error {
	latest := map[Key]Source{}
	for _, s := range sources {
		if cur, ok := latest[s.Key]; !ok || s.Version > cur.Version {
			latest[s.Key] = s
		}
	}
	var errs []error
	next := make(map[Key]Entry, len(latest))
	for k, s := range latest {
		sch, err := compile(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s/%s v%d: %w", k.Tenant, k.EventType, s.Version, err))
			continue
		}
		next[k] = Entry{Version: s.Version, schema: sch}
	}
	r.mu.Lock()
	r.entries = next
	r.mu.Unlock()
	return errs
}

func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

func compile(s Source) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(s.Schema)))
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("lumen://%s/%s/v%d.json", s.Tenant, s.EventType, s.Version)
	c := jsonschema.NewCompiler()
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	return c.Compile(url)
}

var english = message.NewPrinter(language.English)

// flatten turns a nested validation error into one readable line.
func flatten(err error) string {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return err.Error()
	}
	var parts []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := "/" + strings.Join(e.InstanceLocation, "/")
			parts = append(parts, fmt.Sprintf("%s: %s", loc, e.ErrorKind.LocalizedString(english)))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	return strings.Join(parts, "; ")
}

// PGLoader reads every schema version from Postgres.
func PGLoader(pool *pgxpool.Pool) func(context.Context) ([]Source, error) {
	return func(ctx context.Context) ([]Source, error) {
		rows, err := pool.Query(ctx, `SELECT tenant_id, event_type, version, schema FROM event_schemas`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Source
		for rows.Next() {
			var s Source
			if err := rows.Scan(&s.Tenant, &s.EventType, &s.Version, &s.Schema); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, rows.Err()
	}
}

// Refresh loads schemas now and then every interval until ctx is cancelled, so
// a schema registered through the catalog API is enforced within one interval.
func (r *Registry) Refresh(ctx context.Context, load func(context.Context) ([]Source, error), every time.Duration, log *slog.Logger) error {
	reload := func() error {
		src, err := load(ctx)
		if err != nil {
			return err
		}
		for _, e := range r.Load(src) {
			log.Warn("skipping schema", "err", e)
		}
		return nil
	}
	if err := reload(); err != nil {
		return err
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := reload(); err != nil {
					log.Error("schema refresh failed", "err", err)
				}
			}
		}
	}()
	return nil
}
