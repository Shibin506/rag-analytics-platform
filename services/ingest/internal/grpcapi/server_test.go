package grpcapi_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	ingestv1 "github.com/Shibin506/rag-analytics-platform/services/ingest/gen/ingestv1"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/auth"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/core"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/docs"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/grpcapi"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/httpapi"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/objstore"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/schema"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/sink"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/stats"
)

type fakeDocs struct{ titles []string }

func (f *fakeDocs) Put(_ context.Context, tenant, title, text string) (string, error) {
	if err := docs.Validate(title, text); err != nil {
		return "", err
	}
	f.titles = append(f.titles, tenant+":"+title)
	return "doc-1", nil
}

type harness struct {
	svc   *core.Service
	mem   *objstore.Memory
	batch *sink.Batcher
	docs  *fakeDocs
	authn auth.Static
}

func newHarness(t *testing.T) *harness {
	reg := schema.NewRegistry()
	reg.Load([]schema.Source{{
		Key: schema.Key{Tenant: "acme", EventType: "page.view"}, Version: 1,
		Schema: json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`),
	}})
	mem := objstore.NewMemory()
	b := sink.NewBatcher(mem, sink.Options{MaxBytes: 1 << 20, MaxAge: time.Hour})
	return &harness{
		svc: core.New(reg, b, stats.NewMinutely(), 1000), mem: mem, batch: b,
		docs: &fakeDocs{}, authn: auth.Static{"k-acme": "acme"},
	}
}

func view(i int, path string) *ingestv1.Event {
	return &ingestv1.Event{
		EventId:        "8d3f7c62-5b1e-4f7e-9a51-1f0c2b3d4e" + string(rune('0'+i/10)) + string(rune('0'+i%10)),
		EventType:      "page.view",
		OccurredAt:     time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
		PropertiesJson: `{"path":"` + path + `"}`,
	}
}

func dial(t *testing.T, h *harness) ingestv1.IngestServiceClient {
	lis := bufconn.Listen(1 << 20)
	srv := grpcapi.New(&grpcapi.Server{Core: h.svc, Docs: h.docs, StreamChunk: 3}, h.authn)
	go srv.Serve(lis) //nolint:errcheck
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///buf",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return ingestv1.NewIngestServiceClient(conn)
}

func withKey(key string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "x-api-key", key)
}

func TestGRPCRequiresAPIKey(t *testing.T) {
	c := dial(t, newHarness(t))
	_, err := c.SendEvents(withKey("wrong"), &ingestv1.SendEventsRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got %v", err)
	}
}

func TestGRPCStreamReportsRejectionsWithStreamIndexes(t *testing.T) {
	h := newHarness(t)
	c := dial(t, h)
	stream, err := c.StreamEvents(withKey("k-acme"))
	if err != nil {
		t.Fatal(err)
	}
	// 8 events in chunks of 3; events 4 and 7 are invalid.
	for i := 0; i < 8; i++ {
		e := view(i, "/p")
		if i == 4 || i == 7 {
			e.PropertiesJson = `{}`
		}
		if err := stream.Send(e); err != nil {
			t.Fatal(err)
		}
	}
	res, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 6 || len(res.Rejected) != 2 || res.Rejected[0].Index != 4 || res.Rejected[1].Index != 7 {
		t.Fatalf("got %+v", res)
	}
	_ = h.batch.FlushAll(context.Background())
	n := 0
	for _, k := range h.mem.Keys() {
		n += strings.Count(string(h.mem.Objects[k]), "\n")
	}
	if n != 6 {
		t.Fatalf("stored %d rows", n)
	}
}

func TestGRPCPutDocumentValidates(t *testing.T) {
	h := newHarness(t)
	c := dial(t, h)
	if _, err := c.PutDocument(withKey("k-acme"), &ingestv1.PutDocumentRequest{Title: "", Text: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v", err)
	}
	res, err := c.PutDocument(withKey("k-acme"), &ingestv1.PutDocumentRequest{Title: "Refunds", Text: "Refunds take 5 days."})
	if err != nil || res.DocumentId != "doc-1" || h.docs.titles[0] != "acme:Refunds" {
		t.Fatalf("%v %v %v", res, err, h.docs.titles)
	}
}

func TestRESTEventsAndDocuments(t *testing.T) {
	h := newHarness(t)
	srv := httptest.NewServer((&httpapi.Server{Core: h.svc, Docs: h.docs, Auth: h.authn}).Handler())
	defer srv.Close()

	post := func(path, key, body string) (*http.Response, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("X-API-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}

	e, _ := json.Marshal(map[string]any{"events": []map[string]any{
		{"event_id": "8d3f7c62-5b1e-4f7e-9a51-1f0c2b3d4e5f", "event_type": "page.view",
			"occurred_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), "properties": map[string]any{"path": "/"}},
		{"event_id": "x", "event_type": "page.view"},
	}})
	if resp, _ := post("/v1/events", "nope", string(e)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
	resp, out := post("/v1/events", "k-acme", string(e))
	if resp.StatusCode != http.StatusOK || out["accepted"].(float64) != 1 || len(out["rejected"].([]any)) != 1 {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	if resp, _ := post("/v1/events", "k-acme", "not json"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
	resp, out = post("/v1/documents", "k-acme", `{"title":"FAQ","text":"Hello"}`)
	if resp.StatusCode != http.StatusAccepted || out["document_id"] != "doc-1" {
		t.Fatalf("%d %v", resp.StatusCode, out)
	}
	if resp, _ := post("/v1/documents", "k-acme", `{"title":"FAQ","text":"  "}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
