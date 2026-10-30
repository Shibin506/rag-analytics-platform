package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/core"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/schema"
)

const orderSchema = `{
	"type": "object",
	"required": ["order_id", "amount_cents", "currency"],
	"properties": {
		"order_id": {"type": "string"},
		"amount_cents": {"type": "integer", "minimum": 0},
		"currency": {"type": "string", "pattern": "^[A-Z]{3}$"}
	},
	"additionalProperties": false
}`

type line struct {
	tenant, day string
	body        map[string]any
}

type fakeSink struct {
	lines []line
	err   error
}

func (f *fakeSink) Add(_ context.Context, tenant string, day time.Time, b []byte) error {
	if f.err != nil {
		return f.err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	f.lines = append(f.lines, line{tenant, day.Format("2006-01-02"), m})
	return nil
}

type fakeStats struct{ accepted, rejected int }

func (f *fakeStats) Record(_, _ string, ok bool) {
	if ok {
		f.accepted++
	} else {
		f.rejected++
	}
}

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func newService(t *testing.T) (*core.Service, *fakeSink, *fakeStats) {
	t.Helper()
	reg := schema.NewRegistry()
	if errs := reg.Load([]schema.Source{
		{Key: schema.Key{Tenant: "acme", EventType: "order.placed"}, Version: 1, Schema: json.RawMessage(`{"type":"object"}`)},
		{Key: schema.Key{Tenant: "acme", EventType: "order.placed"}, Version: 2, Schema: json.RawMessage(orderSchema)},
	}); len(errs) > 0 {
		t.Fatal(errs)
	}
	s, st := &fakeSink{}, &fakeStats{}
	svc := core.New(reg, s, st, 100)
	svc.Now = func() time.Time { return now }
	return svc, s, st
}

func order(id, at, props string) core.Event {
	return core.Event{EventID: id, EventType: "order.placed", OccurredAt: at, UserID: "u1", Properties: json.RawMessage(props)}
}

const goodProps = `{"order_id":"o1","amount_cents":1250,"currency":"USD"}`

func TestIngestAcceptsValidAndRejectsInvalidIndependently(t *testing.T) {
	svc, sink, st := newService(t)
	res, err := svc.Ingest(context.Background(), "acme", []core.Event{
		order("8d3f7c62-5b1e-4f7e-9a51-1f0c2b3d4e5f", "2026-10-29T23:59:59Z", goodProps),
		order("not-a-uuid", "2026-10-30T10:00:00Z", goodProps),
		order("7b0e1d2c-3a4f-4b5c-8d6e-9f0a1b2c3d4e", "2026-10-30T10:00:00Z", `{"order_id":"o2","amount_cents":"12.50","currency":"usd"}`),
		{EventID: "6a9d0c1b-2e3f-4a5b-9c7d-8e9f0a1b2c3d", EventType: "checkout.started", OccurredAt: "2026-10-30T10:00:00Z", Properties: json.RawMessage(`{}`)},
		order("5f8c9b0a-1d2e-4f3a-8b4c-7d8e9f0a1b2c", "2026-10-30T10:00:00+02:00", goodProps),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 2 || len(res.Rejected) != 3 {
		t.Fatalf("accepted=%d rejected=%v", res.Accepted, res.Rejected)
	}
	reasons := map[int]string{}
	for _, r := range res.Rejected {
		reasons[r.Index] = r.Reason
	}
	if !strings.Contains(reasons[1], "UUID") {
		t.Errorf("index 1: %q", reasons[1])
	}
	if !strings.Contains(reasons[2], "schema v2") || !strings.Contains(reasons[2], "/amount_cents") || !strings.Contains(reasons[2], "/currency") {
		t.Errorf("index 2 should name both bad fields and the version: %q", reasons[2])
	}
	if !strings.Contains(reasons[3], "not registered") {
		t.Errorf("index 3: %q", reasons[3])
	}
	if st.accepted != 2 || st.rejected != 3 {
		t.Errorf("stats %+v", st)
	}

	// The partition is the event's own UTC day, not the ingest day.
	if sink.lines[0].day != "2026-10-29" || sink.lines[1].day != "2026-10-30" {
		t.Errorf("days: %s %s", sink.lines[0].day, sink.lines[1].day)
	}
	got := sink.lines[1].body
	if got["occurred_at"] != "2026-10-30T08:00:00Z" || got["schema_version"].(float64) != 2 || got["tenant_id"] != "acme" {
		t.Errorf("record not normalised: %v", got)
	}
}

func TestIngestRejectsBadTimestamps(t *testing.T) {
	svc, _, _ := newService(t)
	cases := map[string]string{
		"yesterday-ish":   "",
		"2026-10-30 10:00": "RFC 3339",
		"2026-10-30T13:00:00Z": "future",
		"2024-01-01T00:00:00Z": "too old",
	}
	for at, want := range cases {
		if want == "" {
			continue
		}
		res, err := svc.Ingest(context.Background(), "acme", []core.Event{order("8d3f7c62-5b1e-4f7e-9a51-1f0c2b3d4e5f", at, goodProps)})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0].Reason, want) {
			t.Errorf("%s: %+v", at, res.Rejected)
		}
	}
}

func TestIngestRejectsNonObjectProperties(t *testing.T) {
	svc, _, _ := newService(t)
	for _, p := range []string{`[1,2]`, `"x"`, `{bad json`} {
		res, _ := svc.Ingest(context.Background(), "acme", []core.Event{order("8d3f7c62-5b1e-4f7e-9a51-1f0c2b3d4e5f", "2026-10-30T10:00:00Z", p)})
		if len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0].Reason, "properties") {
			t.Errorf("%s: %+v", p, res.Rejected)
		}
	}
}

func TestIngestSchemasAreTenantScoped(t *testing.T) {
	svc, _, _ := newService(t)
	res, _ := svc.Ingest(context.Background(), "globex", []core.Event{order("8d3f7c62-5b1e-4f7e-9a51-1f0c2b3d4e5f", "2026-10-30T10:00:00Z", goodProps)})
	if res.Accepted != 0 {
		t.Fatal("globex must not use acme's schema")
	}
}

func TestIngestLimitsBatchSizeAndSurfacesSinkErrors(t *testing.T) {
	svc, sink, _ := newService(t)
	svc.MaxBatch = 1
	_, err := svc.Ingest(context.Background(), "acme", make([]core.Event, 2))
	if !errors.Is(err, core.ErrBatchTooLarge) {
		t.Fatalf("got %v", err)
	}
	svc.MaxBatch = 10
	sink.err = errors.New("s3 down")
	_, err = svc.Ingest(context.Background(), "acme", []core.Event{order("8d3f7c62-5b1e-4f7e-9a51-1f0c2b3d4e5f", "2026-10-30T10:00:00Z", goodProps)})
	if err == nil || !strings.Contains(err.Error(), "s3 down") {
		t.Fatalf("got %v", err)
	}
}
