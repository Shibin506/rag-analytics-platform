// Package docs accepts documents for retrieval: the text goes to S3, a row goes
// to Postgres, and a NOTIFY wakes the RAG indexer.
package docs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/objstore"
)

const MaxBytes = 2 << 20

var ErrInvalid = errors.New("invalid document")

type Uploader interface {
	Put(ctx context.Context, tenant, title, text string) (string, error)
}

type Store struct {
	Pool  *pgxpool.Pool
	Store objstore.Store
}

// Validate is shared by both transports.
func Validate(title, text string) error {
	switch {
	case strings.TrimSpace(title) == "":
		return fmt.Errorf("%w: title is required", ErrInvalid)
	case len(title) > 300:
		return fmt.Errorf("%w: title is longer than 300 characters", ErrInvalid)
	case strings.TrimSpace(text) == "":
		return fmt.Errorf("%w: text is empty", ErrInvalid)
	case len(text) > MaxBytes:
		return fmt.Errorf("%w: text is larger than %d bytes", ErrInvalid, MaxBytes)
	case !utf8.ValidString(text):
		return fmt.Errorf("%w: text is not valid UTF-8", ErrInvalid)
	}
	return nil
}

func (s *Store) Put(ctx context.Context, tenant, title, text string) (string, error) {
	if err := Validate(title, text); err != nil {
		return "", err
	}
	id := uuid.Must(uuid.NewV7()).String()
	key := fmt.Sprintf("docs/%s/%s.txt", tenant, id)
	if err := s.Store.Put(ctx, key, []byte(text), "text/plain; charset=utf-8"); err != nil {
		return "", fmt.Errorf("storing document: %w", err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx,
		`INSERT INTO documents (id, tenant_id, title, s3_key) VALUES ($1, $2, $3, $4)`,
		id, tenant, strings.TrimSpace(title), key); err != nil {
		return "", err
	}
	// Delivered on commit, so the indexer never sees an ID before its row.
	if _, err := tx.Exec(ctx, `SELECT pg_notify('documents_pending', $1)`, id); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}
