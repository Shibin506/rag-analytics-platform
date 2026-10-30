// Command loadgen streams realistic synthetic events into the gateway over
// gRPC and uploads sample documents over REST, so the whole platform has data
// to work with.
//
//	go run ./cmd/loadgen -days 14 -per-day 20000 -docs ../../samples/docs
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	ingestv1 "github.com/Shibin506/rag-analytics-platform/services/ingest/gen/ingestv1"
)

func main() {
	grpcAddr := flag.String("grpc", "localhost:9090", "gateway gRPC address")
	httpAddr := flag.String("http", "http://localhost:8080", "gateway REST base URL")
	keys := flag.String("keys", "lumen_acme_demo_key,lumen_globex_demo_key", "comma-separated tenant API keys")
	days := flag.Int("days", 14, "days of history to generate, ending today")
	perDay := flag.Int("per-day", 20000, "events per tenant per day")
	invalid := flag.Float64("invalid", 0.02, "fraction of deliberately invalid events")
	docsDir := flag.String("docs", "", "directory of .md/.txt files to upload as documents")
	seed := flag.Uint64("seed", 42, "random seed")
	flag.Parse()

	ctx := context.Background()
	conn, err := grpc.NewClient(*grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	client := ingestv1.NewIngestServiceClient(conn)
	rng := rand.New(rand.NewPCG(*seed, *seed^0x9e3779b97f4a7c15))

	for _, key := range strings.Split(*keys, ",") {
		start := time.Now()
		accepted, rejected := 0, 0
		for d := *days - 1; d >= 0; d-- {
			day := time.Now().UTC().Truncate(24 * time.Hour).AddDate(0, 0, -d)
			res, err := streamDay(ctx, client, key, day, *perDay, *invalid, rng)
			if err != nil {
				log.Fatalf("streaming %s: %v", day.Format("2006-01-02"), err)
			}
			accepted += int(res.Accepted)
			rejected += len(res.Rejected)
		}
		fmt.Printf("%s: %d accepted, %d rejected in %s\n", key, accepted, rejected, time.Since(start).Round(time.Millisecond))
	}

	if *docsDir != "" {
		if err := uploadDocs(*httpAddr, strings.Split(*keys, ",")[0], *docsDir); err != nil {
			log.Fatal(err)
		}
	}
}

func streamDay(ctx context.Context, c ingestv1.IngestServiceClient, key string, day time.Time, n int, invalid float64, rng *rand.Rand) (*ingestv1.SendEventsResponse, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "x-api-key", key)
	stream, err := c.StreamEvents(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		at := day.Add(time.Duration(diurnal(rng)) * time.Second)
		if at.After(now) {
			at = now.Add(-time.Duration(rng.IntN(3600)) * time.Second)
		}
		if err := stream.Send(randomEvent(rng, at, rng.Float64() < invalid)); err != nil {
			return nil, err
		}
	}
	return stream.CloseAndRecv()
}

// diurnal picks a second of the day with more traffic in the afternoon.
func diurnal(rng *rand.Rand) int {
	for {
		s := rng.IntN(86400)
		hour := float64(s) / 3600
		weight := 0.25 + 0.75*gauss(hour, 15, 4)
		if rng.Float64() < weight {
			return s
		}
	}
}

func gauss(x, mu, sigma float64) float64 {
	d := (x - mu) / sigma
	return 1 / (1 + d*d)
}

var paths = []string{"/", "/pricing", "/docs", "/docs/api", "/blog", "/checkout", "/account", "/search"}
var topics = []string{"billing", "login", "api", "export", "performance", "integration"}
var priorities = []string{"low", "normal", "normal", "high", "urgent"}

func randomEvent(rng *rand.Rand, at time.Time, broken bool) *ingestv1.Event {
	user := fmt.Sprintf("u_%05d", rng.IntN(25000))
	var typ string
	var props map[string]any
	switch r := rng.Float64(); {
	case r < 0.80:
		typ = "page.view"
		props = map[string]any{"path": paths[rng.IntN(len(paths))], "duration_ms": rng.IntN(120000)}
		if rng.Float64() < 0.3 {
			props["referrer"] = "https://www.google.com/"
		}
	case r < 0.95:
		typ = "order.placed"
		props = map[string]any{
			"order_id": "o_" + uuid.NewString()[:8], "amount_cents": 500 + rng.IntN(25000),
			"currency": []string{"USD", "USD", "EUR", "GBP"}[rng.IntN(4)], "items": 1 + rng.IntN(5),
		}
	default:
		typ = "support.ticket"
		props = map[string]any{
			"ticket_id": "t_" + uuid.NewString()[:8], "priority": priorities[rng.IntN(len(priorities))],
			"topic": topics[rng.IntN(len(topics))],
		}
	}
	if broken {
		// The kinds of mistakes real clients make.
		switch rng.IntN(3) {
		case 0:
			props["amount_cents"] = "12.50"
			typ = "order.placed"
			props["order_id"], props["currency"] = "o_bad", "usd"
		case 1:
			delete(props, "path")
			typ = "page.view"
			delete(props, "amount_cents")
		default:
			typ = "checkout.started" // never registered
		}
	}
	b, _ := json.Marshal(props)
	return &ingestv1.Event{
		EventId: uuid.NewString(), EventType: typ, OccurredAt: at.Format(time.RFC3339Nano),
		UserId: user, PropertiesJson: string(b),
	}
}

func uploadDocs(base, key, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		return err
	}
	for _, f := range files {
		if ext := filepath.Ext(f); ext != ".md" && ext != ".txt" {
			continue
		}
		text, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		title := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		if first, _, ok := strings.Cut(string(text), "\n"); ok && strings.HasPrefix(first, "# ") {
			title = strings.TrimPrefix(first, "# ")
		}
		body, _ := json.Marshal(map[string]string{"title": title, "text": string(text)})
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/documents", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		var out map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			return fmt.Errorf("uploading %s: %s %v", f, resp.Status, out)
		}
		fmt.Printf("document %q -> %s\n", title, out["document_id"])
	}
	return nil
}
