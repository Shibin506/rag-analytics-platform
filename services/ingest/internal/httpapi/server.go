// Package httpapi exposes the ingest service as a JSON REST API, for clients
// that cannot speak gRPC (browsers, webhooks, curl).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/auth"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/core"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/docs"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/stats"
)

type Server struct {
	Core  *core.Service
	Docs  docs.Uploader
	Auth  auth.Authenticator
	Ready func(context.Context) error
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/events", s.timed("events", s.authed(s.postEvents)))
	mux.Handle("POST /v1/documents", s.timed("documents", s.authed(s.postDocument)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.Ready != nil {
			if err := s.Ready(r.Context()); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "down", "error": err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	return cors(mux)
}

type ctxKey struct{}

func (s *Server) authed(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant, err := s.Auth.Tenant(r.Context(), r.Header.Get("X-API-Key"))
		if errors.Is(err, auth.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, tenant)), tenant)
	}
}

func (s *Server) timed(name string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		stats.RequestSeconds.WithLabelValues("http", name).Observe(time.Since(start).Seconds())
	})
}

type eventsBody struct {
	Events []core.Event `json:"events"`
}

func (s *Server) postEvents(w http.ResponseWriter, r *http.Request, tenant string) {
	var body eventsBody
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("body must be {\"events\": [...]}"))
		return
	}
	res, err := s.Core.Ingest(r.Context(), tenant, body.Events)
	switch {
	case errors.Is(err, core.ErrBatchTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err)
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, err)
	default:
		code := http.StatusOK
		if res.Accepted == 0 && len(res.Rejected) > 0 {
			code = http.StatusUnprocessableEntity
		}
		writeJSON(w, code, res)
	}
}

type documentBody struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

func (s *Server) postDocument(w http.ResponseWriter, r *http.Request, tenant string) {
	var body documentBody
	r.Body = http.MaxBytesReader(w, r.Body, docs.MaxBytes+4096)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("body must be {\"title\": ..., \"text\": ...}"))
		return
	}
	id, err := s.Docs.Put(r.Context(), tenant, body.Title, body.Text)
	switch {
	case errors.Is(err, docs.ErrInvalid):
		writeError(w, http.StatusBadRequest, err)
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"document_id": id, "status": "pending"})
	}
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
