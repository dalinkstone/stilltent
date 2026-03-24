package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/qiffang/mnemos/server/internal/domain"
	"github.com/qiffang/mnemos/server/internal/embed"
	"github.com/qiffang/mnemos/server/internal/llm"
	"github.com/qiffang/mnemos/server/internal/metrics"
	"github.com/qiffang/mnemos/server/internal/middleware"
	"github.com/qiffang/mnemos/server/internal/repository"
	"github.com/qiffang/mnemos/server/internal/service"
)

// maxRequestBodySize is the default maximum request body size for JSON endpoints (10MB).
const maxRequestBodySize = 10 << 20

// defaultRequestTimeout is the per-request context timeout for handler goroutines.
const defaultRequestTimeout = 30 * time.Second

// healthBody is the pre-allocated JSON response for the health endpoint.
// Avoids json.Marshal and allocation on every health check.
var healthBody = []byte(`{"status":"ok"}` + "\n")

// gzipWriterPool reuses gzip.Writers to avoid allocation per request.
var gzipWriterPool = sync.Pool{
	New: func() any {
		gz, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		return gz
	},
}

// jsonBufPool provides reusable byte buffers for JSON marshalling,
// reducing GC pressure under high request rates.
var jsonBufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// gzipResponseWriter wraps http.ResponseWriter to write through a gzip.Writer.
type gzipResponseWriter struct {
	http.ResponseWriter
	Writer io.Writer
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	return g.Writer.Write(b)
}

// gzipMiddleware compresses JSON responses for clients that accept gzip encoding.
// It uses a sync.Pool for gzip writers to minimise allocations.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		gz := gzipWriterPool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			gz.Close()
			gzipWriterPool.Put(gz)
		}()

		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		// Delete Content-Length since compressed size will differ.
		w.Header().Del("Content-Length")

		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, Writer: gz}, r)
	})
}

// requestTimeoutMiddleware adds a context timeout to every request so no single
// handler goroutine can block indefinitely.
func requestTimeoutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), defaultRequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Server holds the HTTP handlers and their dependencies.
type Server struct {
	tenant      *service.TenantService
	uploadTasks repository.UploadTaskRepo
	uploadDir   string
	embedder    *embed.Embedder
	llmClient   *llm.Client
	autoModel   string
	ftsEnabled  bool
	ingestMode  service.IngestMode
	dbBackend   string
	logger      *slog.Logger
	svcCache    sync.Map
}

// NewServer creates a new HTTP handler server.
func NewServer(
	tenantSvc *service.TenantService,
	uploadTasks repository.UploadTaskRepo,
	uploadDir string,
	embedder *embed.Embedder,
	llmClient *llm.Client,
	autoModel string,
	ftsEnabled bool,
	ingestMode service.IngestMode,
	dbBackend string,
	logger *slog.Logger,
) *Server {
	return &Server{
		tenant:      tenantSvc,
		uploadTasks: uploadTasks,
		uploadDir:   uploadDir,
		embedder:    embedder,
		llmClient:   llmClient,
		autoModel:   autoModel,
		ftsEnabled:  ftsEnabled,
		ingestMode:  ingestMode,
		dbBackend:   dbBackend,
		logger:      logger,
	}
}

// resolvedSvc holds the correct service instances for a request.
// Services are always backed by the tenant's dedicated DB.
type resolvedSvc struct {
	memory  *service.MemoryService
	ingest  *service.IngestService
	session *service.SessionService
}

type tenantSvcKey string

// resolveServices returns the correct services for a request.
func (s *Server) resolveServices(auth *domain.AuthInfo) resolvedSvc {
	if auth.TenantID == "" {
		key := tenantSvcKey(fmt.Sprintf("db-%p", auth.TenantDB))
		if cached, ok := s.svcCache.Load(key); ok {
			return cached.(resolvedSvc)
		}
		memRepo := repository.NewMemoryRepo(s.dbBackend, auth.TenantDB, s.autoModel, s.ftsEnabled, auth.ClusterID)
		sessRepo := repository.NewSessionRepo(s.dbBackend, auth.TenantDB, s.autoModel, s.ftsEnabled, auth.ClusterID)
		svc := resolvedSvc{
			memory:  service.NewMemoryService(memRepo, s.llmClient, s.embedder, s.autoModel, s.ingestMode),
			ingest:  service.NewIngestService(memRepo, s.llmClient, s.embedder, s.autoModel, s.ingestMode),
			session: service.NewSessionService(sessRepo, s.embedder, s.autoModel),
		}
		actual, loaded := s.svcCache.LoadOrStore(key, svc)
		if !loaded {
			go func() {
				if err := s.tenant.EnsureSessionsTable(context.Background(), auth.TenantDB); err != nil {
					s.logger.Warn("sessions table migration failed",
						"cluster_id", auth.ClusterID,
						"err", err) // no tenant field: TenantID is empty in this branch
				}
			}()
		}
		return actual.(resolvedSvc)
	}
	key := tenantSvcKey(fmt.Sprintf("%s-%p", auth.TenantID, auth.TenantDB))
	if cached, ok := s.svcCache.Load(key); ok {
		return cached.(resolvedSvc)
	}
	memRepo := repository.NewMemoryRepo(s.dbBackend, auth.TenantDB, s.autoModel, s.ftsEnabled, auth.ClusterID)
	sessRepo := repository.NewSessionRepo(s.dbBackend, auth.TenantDB, s.autoModel, s.ftsEnabled, auth.ClusterID)
	svc := resolvedSvc{
		memory:  service.NewMemoryService(memRepo, s.llmClient, s.embedder, s.autoModel, s.ingestMode),
		ingest:  service.NewIngestService(memRepo, s.llmClient, s.embedder, s.autoModel, s.ingestMode),
		session: service.NewSessionService(sessRepo, s.embedder, s.autoModel),
	}
	actual, loaded := s.svcCache.LoadOrStore(key, svc)
	if !loaded {
		go func() {
			if err := s.tenant.EnsureSessionsTable(context.Background(), auth.TenantDB); err != nil {
				s.logger.Warn("sessions table migration failed",
					"cluster_id", auth.ClusterID,
					"tenant", auth.TenantID,
					"err", err)
			}
		}()
	}
	return actual.(resolvedSvc)
}

// Router builds the chi router with all routes and middleware.
func (s *Server) Router(
	tenantMW func(http.Handler) http.Handler,
	rateLimitMW func(http.Handler) http.Handler,
	apiKeyMW func(http.Handler) http.Handler,
) http.Handler {
	r := chi.NewRouter()

	// Global middleware.
	r.Use(chimw.Recoverer)
	r.Use(chimw.RequestID)
	r.Use(requestLogger(s.logger))
	r.Use(rateLimitMW)
	r.Use(metrics.Middleware)
	r.Use(gzipMiddleware)
	r.Use(requestTimeoutMiddleware)

	// Health check — pre-allocated bytes, no JSON marshal, no DB call.
	// Docker probes this every 15s; it must be as fast as possible.
	// Both /health (Docker) and /healthz (k8s, scripts) are supported.
	healthHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(healthBody)
	}
	r.Get("/health", healthHandler)
	r.Get("/healthz", healthHandler)

	r.Get("/metrics", promhttp.Handler().ServeHTTP)

	// Provision a new tenant — no auth, no body.
	r.Post("/v1alpha1/mem9s", s.provisionMem9s)

	// Tenant-scoped routes — tenantMW resolves {tenantID} to DB connection.
	r.Route("/v1alpha1/mem9s/{tenantID}", func(r chi.Router) {
		r.Use(tenantMW)

		// Memory CRUD.
		r.Post("/memories", s.createMemory)
		r.Get("/memories", s.listMemories)
		r.Get("/memories/{id}", s.getMemory)
		r.Put("/memories/{id}", s.updateMemory)
		r.Delete("/memories/{id}", s.deleteMemory)

		// Imports (async file ingest).
		r.Post("/imports", s.createTask)
		r.Get("/imports", s.listTasks)
		r.Get("/imports/{id}", s.getTask)

		// Session messages (raw captured turns).
		r.Get("/session-messages", s.handleListSessionMessages)
	})

	r.Route("/v1alpha2/mem9s", func(r chi.Router) {
		r.Use(apiKeyMW)

		r.Post("/memories", s.createMemory)
		r.Get("/memories", s.listMemories)
		r.Get("/memories/{id}", s.getMemory)
		r.Put("/memories/{id}", s.updateMemory)
		r.Delete("/memories/{id}", s.deleteMemory)

		r.Post("/imports", s.createTask)
		r.Get("/imports", s.listTasks)
		r.Get("/imports/{id}", s.getTask)

		// Session messages (raw captured turns).
		r.Get("/session-messages", s.handleListSessionMessages)
	})

	return r
}

// respond writes a JSON response using a pooled buffer to reduce GC pressure.
// The buffer is marshalled first so that encoding errors are caught before
// writing the HTTP status code, preventing partial/corrupt responses.
func respond(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	if data == nil {
		w.WriteHeader(status)
		return
	}

	buf := jsonBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer jsonBufPool.Put(buf)

	if err := json.NewEncoder(buf).Encode(data); err != nil {
		slog.Error("failed to encode response", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal server error"}` + "\n"))
		return
	}

	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// errorResponse is the structured error format returned by all error paths.
type errorResponse struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

// respondError writes a structured JSON error response with consistent format.
func respondError(w http.ResponseWriter, status int, msg string) {
	respond(w, status, errorResponse{Error: msg, Code: status})
}

// handleError maps domain errors to HTTP status codes.
func (s *Server) handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		respondError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, domain.ErrWriteConflict):
		respondError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, domain.ErrConflict):
		respondError(w, http.StatusConflict, err.Error())
	case errors.Is(err, domain.ErrDuplicateKey):
		respondError(w, http.StatusConflict, "duplicate key: "+err.Error())
	case errors.Is(err, domain.ErrValidation):
		respondError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, domain.ErrNotSupported):
		respondError(w, http.StatusNotImplemented, err.Error())
	default:
		s.logger.Error("internal error", "err", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
	}
}

// decode reads and JSON-decodes the request body.
// It applies MaxBytesReader to prevent memory exhaustion from oversized payloads.
func decode(r *http.Request, dst any) error {
	if r.Body == nil {
		return &domain.ValidationError{Message: "request body required"}
	}
	// Limit body size to prevent memory exhaustion from oversized JSON payloads.
	// Note: for multipart/file-upload endpoints (e.g. createTask), the handler
	// applies its own limit before calling ParseMultipartForm, so this path
	// only runs for JSON-body endpoints.
	r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodySize)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		// MaxBytesError surfaces as a generic error; give a clear message.
		if err.Error() == "http: request body too large" {
			return &domain.ValidationError{Message: "request body too large (limit 10MB)"}
		}
		return &domain.ValidationError{Message: "invalid JSON: " + err.Error()}
	}
	return nil
}

// authInfo extracts AuthInfo from context.
func authInfo(r *http.Request) *domain.AuthInfo {
	return middleware.AuthFromContext(r.Context())
}

// requestLogger returns a middleware that logs each request.
// It uses the chi route pattern (e.g. /v1alpha1/mem9s/{tenantID}/memories)
// instead of the raw URL path to avoid logging sensitive tenant IDs.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			// Use route pattern to avoid exposing sensitive path params (e.g. tenantID).
			routeCtx := chi.RouteContext(r.Context())
			path := r.URL.Path
			if routeCtx != nil {
				if pattern := routeCtx.RoutePattern(); pattern != "" {
					path = pattern
				}
			}
			logger.Info("request",
				"method", r.Method,
				"path", path,
				"status", ww.Status(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", chimw.GetReqID(r.Context()),
			)
		})
	}
}
