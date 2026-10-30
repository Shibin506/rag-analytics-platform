package sink

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/objstore"
)

func day(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func TestBatcherPartitionsByTenantAndDay(t *testing.T) {
	mem := objstore.NewMemory()
	b := NewBatcher(mem, Options{MaxBytes: 1 << 20, MaxAge: time.Hour})
	ctx := context.Background()
	for _, c := range []struct{ tenant, day, line string }{
		{"acme", "2026-10-29", `{"a":1}`},
		{"acme", "2026-10-29", `{"a":2}`},
		{"acme", "2026-10-30", `{"a":3}`},
		{"globex", "2026-10-29", `{"g":1}`},
	} {
		if err := b.Add(ctx, c.tenant, day(c.day), []byte(c.line)); err != nil {
			t.Fatal(err)
		}
	}
	if len(mem.Keys()) != 0 {
		t.Fatal("nothing should be written before a flush")
	}
	if err := b.FlushAll(ctx); err != nil {
		t.Fatal(err)
	}
	keys := mem.Keys()
	if len(keys) != 3 {
		t.Fatalf("keys: %v", keys)
	}
	var acme29 string
	for _, k := range keys {
		if !strings.HasPrefix(k, "raw/events/tenant=") || !strings.HasSuffix(k, ".json") {
			t.Errorf("bad key %s", k)
		}
		if strings.Contains(k, "tenant=acme/dt=2026-10-29/") {
			acme29 = string(mem.Objects[k])
		}
	}
	if acme29 != "{\"a\":1}\n{\"a\":2}\n" {
		t.Errorf("acme 29th body = %q", acme29)
	}
	if b.Pending() != 0 {
		t.Error("buffers should be empty")
	}
}

func TestBatcherFlushesWhenFullAndWhenOld(t *testing.T) {
	mem := objstore.NewMemory()
	b := NewBatcher(mem, Options{MaxBytes: 20, MaxAge: time.Minute})
	clock := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return clock }
	ctx := context.Background()

	_ = b.Add(ctx, "acme", day("2026-10-30"), []byte(`{"n":1}`)) // 8 bytes
	_ = b.Add(ctx, "acme", day("2026-10-30"), []byte(`{"n":2}`)) // 16 bytes
	if len(mem.Keys()) != 0 {
		t.Fatal("under the size limit")
	}
	_ = b.Add(ctx, "acme", day("2026-10-30"), []byte(`{"n":3}`)) // 24 bytes: flush
	if len(mem.Keys()) != 1 {
		t.Fatalf("size flush: %v", mem.Keys())
	}

	_ = b.Add(ctx, "acme", day("2026-10-30"), []byte(`{"n":4}`))
	_ = b.FlushDue(ctx)
	if len(mem.Keys()) != 1 {
		t.Fatal("not old enough yet")
	}
	clock = clock.Add(2 * time.Minute)
	_ = b.FlushDue(ctx)
	if len(mem.Keys()) != 2 {
		t.Fatalf("age flush: %v", mem.Keys())
	}
}

func TestBatcherKeepsRowsWhenUploadFails(t *testing.T) {
	mem := objstore.NewMemory()
	mem.FailNext = 1
	b := NewBatcher(mem, Options{MaxBytes: 1 << 20, MaxAge: time.Hour})
	ctx := context.Background()
	_ = b.Add(ctx, "acme", day("2026-10-30"), []byte(`{"n":1}`))
	if err := b.FlushAll(ctx); err == nil {
		t.Fatal("expected the injected failure")
	}
	_ = b.Add(ctx, "acme", day("2026-10-30"), []byte(`{"n":2}`))
	if b.Pending() != 2 {
		t.Fatalf("pending = %d, want 2", b.Pending())
	}
	if err := b.FlushAll(ctx); err != nil {
		t.Fatal(err)
	}
	keys := mem.Keys()
	if len(keys) != 1 || string(mem.Objects[keys[0]]) != "{\"n\":1}\n{\"n\":2}\n" {
		t.Fatalf("retry lost or reordered rows: %v %q", keys, mem.Objects[keys[0]])
	}
}

func TestBatcherConcurrentAddsLoseNothing(t *testing.T) {
	mem := objstore.NewMemory()
	b := NewBatcher(mem, Options{MaxBytes: 512, MaxAge: time.Hour})
	ctx := context.Background()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_ = b.Add(ctx, "acme", day("2026-10-30"), []byte(fmt.Sprintf(`{"w":%d,"i":%d}`, w, i)))
			}
		}(w)
	}
	wg.Wait()
	_ = b.FlushAll(ctx)
	lines := 0
	for _, k := range mem.Keys() {
		lines += strings.Count(string(mem.Objects[k]), "\n")
	}
	if lines != 4000 {
		t.Fatalf("wrote %d lines, want 4000", lines)
	}
}
