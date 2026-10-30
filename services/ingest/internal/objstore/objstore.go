// Package objstore is the gateway's view of S3: put an object, nothing more.
package objstore

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store interface {
	Put(ctx context.Context, key string, body []byte, contentType string) error
}

// S3 works with any S3-compatible service (AWS S3, SeaweedFS, MinIO...).
type S3 struct {
	client *minio.Client
	bucket string
}

func NewS3(endpoint, accessKey, secretKey, bucket, region string, useSSL bool) (*S3, error) {
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return nil, err
	}
	return &S3{client: c, bucket: bucket}, nil
}

// EnsureBucket creates the bucket if it does not exist yet.
func (s *S3) EnsureBucket(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("checking bucket %s: %w", s.bucket, err)
	}
	if ok {
		return nil
	}
	return s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{})
}

func (s *S3) Put(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(body), int64(len(body)),
		minio.PutObjectOptions{ContentType: contentType})
	return err
}

// Memory is an in-process Store for tests.
type Memory struct {
	mu      sync.Mutex
	Objects map[string][]byte
	// FailNext makes the next Put fail, to exercise retry paths.
	FailNext int
}

func NewMemory() *Memory { return &Memory{Objects: map[string][]byte{}} }

func (m *Memory) Put(_ context.Context, key string, body []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailNext > 0 {
		m.FailNext--
		return fmt.Errorf("injected failure")
	}
	m.Objects[key] = append([]byte(nil), body...)
	return nil
}

func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.Objects))
	for k := range m.Objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
