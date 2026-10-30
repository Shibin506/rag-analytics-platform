// Command ingest runs the Lumen ingest gateway: gRPC on :9090, REST on :8080.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/auth"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/config"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/core"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/docs"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/grpcapi"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/httpapi"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/objstore"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/schema"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/sink"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/stats"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("ingest stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	s3, err := objstore.NewS3(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3Region, cfg.S3UseSSL)
	if err != nil {
		return err
	}
	if err := retry(ctx, log, "s3 bucket", func() error { return s3.EnsureBucket(ctx) }); err != nil {
		return err
	}

	registry := schema.NewRegistry()
	if err := retry(ctx, log, "schema registry", func() error {
		return registry.Refresh(ctx, schema.PGLoader(pool), cfg.SchemaRefresh, log)
	}); err != nil {
		return err
	}
	log.Info("schemas loaded", "count", registry.Len())

	minutely := stats.NewMinutely()
	batcher := sink.NewBatcher(s3, sink.Options{
		MaxBytes: cfg.FlushBytes, MaxAge: cfg.FlushInterval, Log: log,
		OnFlushed: func(_ int, n int) {
			stats.FlushedObjects.Inc()
			stats.FlushedBytes.Add(float64(n))
		},
	})
	svc := core.New(registry, batcher, minutely, cfg.MaxBatch)
	authn := auth.NewPG(pool)
	uploader := &docs.Store{Pool: pool, Store: s3}

	// The batcher and stats loops get their own context so they can finish a
	// final flush after the servers have stopped taking requests.
	bg, cancelBG := context.WithCancel(context.Background())
	defer cancelBG()
	batcherDone := make(chan struct{})
	go func() { batcher.Run(bg, time.Second); close(batcherDone) }()
	go func() {
		t := time.NewTicker(cfg.StatsFlush)
		defer t.Stop()
		for {
			select {
			case <-bg.Done():
				return
			case <-t.C:
				if err := minutely.Flush(bg, pool); err != nil {
					log.Error("stats flush failed", "err", err)
				}
			}
		}
	}()

	grpcSrv := grpcapi.New(&grpcapi.Server{Core: svc, Docs: uploader}, authn)
	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: (&httpapi.Server{Core: svc, Docs: uploader, Auth: authn,
			Ready: func(ctx context.Context) error { return pool.Ping(ctx) }}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 2)
	go func() { errc <- grpcSrv.Serve(lis) }()
	go func() {
		if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	log.Info("ingest gateway up", "grpc", cfg.GRPCAddr, "http", cfg.HTTPAddr)

	select {
	case <-ctx.Done():
	case err := <-errc:
		log.Error("server failed", "err", err)
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	grpcSrv.GracefulStop()
	cancelBG()
	<-batcherDone
	if err := minutely.Flush(shutdownCtx, pool); err != nil {
		log.Error("final stats flush failed", "err", err)
	}
	return nil
}

// retry waits for a dependency that may still be starting (compose brings
// everything up at once).
func retry(ctx context.Context, log *slog.Logger, what string, f func() error) error {
	var err error
	for attempt := 1; attempt <= 30; attempt++ {
		if err = f(); err == nil {
			return nil
		}
		log.Warn("waiting for dependency", "what", what, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return err
}
