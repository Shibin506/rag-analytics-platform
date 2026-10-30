// Package grpcapi exposes the ingest service over gRPC.
package grpcapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	ingestv1 "github.com/Shibin506/rag-analytics-platform/services/ingest/gen/ingestv1"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/auth"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/core"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/docs"
	"github.com/Shibin506/rag-analytics-platform/services/ingest/internal/stats"
)

type tenantKey struct{}

// TenantFrom returns the tenant the auth interceptor attached to ctx.
func TenantFrom(ctx context.Context) string {
	t, _ := ctx.Value(tenantKey{}).(string)
	return t
}

type Server struct {
	ingestv1.UnimplementedIngestServiceServer
	Core *core.Service
	Docs docs.Uploader
	// StreamChunk is how many streamed events are buffered before validation.
	StreamChunk int
}

func (s *Server) SendEvents(ctx context.Context, req *ingestv1.SendEventsRequest) (*ingestv1.SendEventsResponse, error) {
	events := make([]core.Event, len(req.GetEvents()))
	for i, e := range req.GetEvents() {
		events[i] = fromProto(e)
	}
	res, err := s.Core.Ingest(ctx, TenantFrom(ctx), events)
	if err != nil {
		return nil, toStatus(err)
	}
	return toProto(res, 0), nil
}

// StreamEvents validates and stores events in chunks as they arrive, so a
// long stream never has to be held in memory.
func (s *Server) StreamEvents(stream grpc.ClientStreamingServer[ingestv1.Event, ingestv1.SendEventsResponse]) error {
	ctx := stream.Context()
	tenant := TenantFrom(ctx)
	chunkSize := s.StreamChunk
	if chunkSize <= 0 {
		chunkSize = 500
	}
	out := &ingestv1.SendEventsResponse{}
	chunk := make([]core.Event, 0, chunkSize)
	offset := 0
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		res, err := s.Core.Ingest(ctx, tenant, chunk)
		if err != nil {
			return toStatus(err)
		}
		part := toProto(res, offset)
		out.Accepted += part.Accepted
		out.Rejected = append(out.Rejected, part.Rejected...)
		offset += len(chunk)
		chunk = chunk[:0]
		return nil
	}
	for {
		e, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if err := flush(); err != nil {
				return err
			}
			return stream.SendAndClose(out)
		}
		if err != nil {
			return err
		}
		chunk = append(chunk, fromProto(e))
		if len(chunk) == chunkSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
}

func (s *Server) PutDocument(ctx context.Context, req *ingestv1.PutDocumentRequest) (*ingestv1.PutDocumentResponse, error) {
	id, err := s.Docs.Put(ctx, TenantFrom(ctx), req.GetTitle(), req.GetText())
	if err != nil {
		return nil, toStatus(err)
	}
	return &ingestv1.PutDocumentResponse{DocumentId: id, Status: "pending"}, nil
}

func fromProto(e *ingestv1.Event) core.Event {
	return core.Event{
		EventID:    e.GetEventId(),
		EventType:  e.GetEventType(),
		OccurredAt: e.GetOccurredAt(),
		UserID:     e.GetUserId(),
		Properties: json.RawMessage(e.GetPropertiesJson()),
	}
}

func toProto(r core.Result, offset int) *ingestv1.SendEventsResponse {
	out := &ingestv1.SendEventsResponse{Accepted: int32(r.Accepted)}
	for _, rj := range r.Rejected {
		out.Rejected = append(out.Rejected, &ingestv1.Rejection{
			Index: int32(rj.Index + offset), EventId: rj.EventID, Reason: rj.Reason,
		})
	}
	return out
}

func toStatus(err error) error {
	switch {
	case errors.Is(err, core.ErrBatchTooLarge), errors.Is(err, docs.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	default:
		return status.Error(codes.Unavailable, err.Error())
	}
}

func apiKey(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("x-api-key"); len(v) > 0 {
		return v[0]
	}
	return ""
}

func authenticate(ctx context.Context, a auth.Authenticator) (context.Context, error) {
	tenant, err := a.Tenant(ctx, apiKey(ctx))
	if errors.Is(err, auth.ErrUnauthorized) {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return context.WithValue(ctx, tenantKey{}, tenant), nil
}

// UnaryInterceptor authenticates and times unary calls.
func UnaryInterceptor(a auth.Authenticator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		start := time.Now()
		defer func() {
			stats.RequestSeconds.WithLabelValues("grpc", info.FullMethod).Observe(time.Since(start).Seconds())
		}()
		ctx, err := authenticate(ctx, a)
		if err != nil {
			return nil, err
		}
		return h(ctx, req)
	}
}

type authedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s authedStream) Context() context.Context { return s.ctx }

// StreamInterceptor authenticates and times streaming calls.
func StreamInterceptor(a auth.Authenticator) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		start := time.Now()
		defer func() {
			stats.RequestSeconds.WithLabelValues("grpc", info.FullMethod).Observe(time.Since(start).Seconds())
		}()
		ctx, err := authenticate(ss.Context(), a)
		if err != nil {
			return err
		}
		return h(srv, authedStream{ss, ctx})
	}
}

// New builds a gRPC server with auth and metrics wired in.
func New(s *Server, a auth.Authenticator) *grpc.Server {
	g := grpc.NewServer(
		grpc.ChainUnaryInterceptor(UnaryInterceptor(a)),
		grpc.ChainStreamInterceptor(StreamInterceptor(a)),
		grpc.MaxRecvMsgSize(16<<20),
	)
	ingestv1.RegisterIngestServiceServer(g, s)
	return g
}
