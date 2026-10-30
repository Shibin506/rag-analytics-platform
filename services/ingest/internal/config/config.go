// Package config reads the gateway's settings from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr    string
	GRPCAddr    string
	DatabaseURL string

	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Bucket    string
	S3UseSSL    bool
	S3Region    string

	// A tenant/day buffer is written to S3 when it reaches FlushBytes or is
	// older than FlushInterval, whichever comes first.
	FlushBytes    int
	FlushInterval time.Duration

	SchemaRefresh time.Duration
	StatsFlush    time.Duration
	MaxBatch      int
}

func FromEnv() (Config, error) {
	c := Config{
		HTTPAddr:      env("INGEST_HTTP_ADDR", ":8080"),
		GRPCAddr:      env("INGEST_GRPC_ADDR", ":9090"),
		DatabaseURL:   env("DATABASE_URL", "postgres://lumen:lumen@localhost:5432/lumen"),
		S3Endpoint:    env("S3_ENDPOINT", "localhost:8333"),
		S3AccessKey:   env("S3_ACCESS_KEY", "lumen"),
		S3SecretKey:   env("S3_SECRET_KEY", "lumen-secret"),
		S3Bucket:      env("S3_BUCKET", "lumen-lake"),
		S3Region:      env("S3_REGION", "us-east-1"),
		FlushBytes:    4 << 20,
		FlushInterval: 5 * time.Second,
		SchemaRefresh: 15 * time.Second,
		StatsFlush:    5 * time.Second,
		MaxBatch:      5000,
	}
	var err error
	if c.S3UseSSL, err = strconv.ParseBool(env("S3_USE_SSL", "false")); err != nil {
		return c, fmt.Errorf("S3_USE_SSL: %w", err)
	}
	if v := os.Getenv("INGEST_FLUSH_BYTES"); v != "" {
		if c.FlushBytes, err = strconv.Atoi(v); err != nil {
			return c, fmt.Errorf("INGEST_FLUSH_BYTES: %w", err)
		}
	}
	if v := os.Getenv("INGEST_FLUSH_INTERVAL"); v != "" {
		if c.FlushInterval, err = time.ParseDuration(v); err != nil {
			return c, fmt.Errorf("INGEST_FLUSH_INTERVAL: %w", err)
		}
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
