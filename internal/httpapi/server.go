package httpapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// RPCHandler abstracts the daemon's request handler to avoid import cycles.
// daemon.Handler implements this implicitly via its Handle method.
type RPCHandler interface {
	Handle(req RPCRequest) RPCResponse
}

// RPCRequest mirrors daemon.Request without importing daemon.
type RPCRequest struct {
	Method string
	Params map[string]any
}

// RPCResponse mirrors daemon.Response without importing daemon.
type RPCResponse struct {
	Result json.RawMessage
	Error  string
}

// Server is the HTTP API for OpenClaw integration.
type Server struct {
	handler   RPCHandler
	authToken string
	logger    *log.Logger
	httpSrv   *http.Server
}

// New creates an HTTP API server with daemon handler for RPC pass-through.
func New(handler RPCHandler, listen, authToken string, logger *log.Logger) *Server {
	s := &Server{
		handler:   handler,
		authToken: authToken,
		logger:    logger,
	}

	mux := http.NewServeMux()

	// Health — no auth required
	mux.HandleFunc("GET /health", s.handleHealth)

	// Assemble endpoint — requires auth
	mux.HandleFunc("POST /api/assemble", BearerAuthFunc(authToken, s.handleAssemble))

	// Analyze-turn endpoint — requires auth
	mux.HandleFunc("POST /api/analyze-turn", BearerAuthFunc(authToken, s.handleAnalyzeTurn))

	// Ingest endpoints — require auth
	mux.HandleFunc("POST /api/ingest", BearerAuthFunc(authToken, s.handleIngest))
	mux.HandleFunc("POST /api/ingest-history", BearerAuthFunc(authToken, s.handleIngestHistory))

	// RPC pass-through — requires auth
	mux.HandleFunc("POST /api/rpc/{method}", BearerAuthFunc(authToken, s.handleRPC))

	s.httpSrv = &http.Server{
		Addr:         listen,
		Handler:      LocalhostOnly(RequestLogger(logger, mux)),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	return s
}

// handleRPC is a generic pass-through for existing daemon RPC methods.
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	method := r.PathValue("method")

	var params map[string]any
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		params = map[string]any{}
	}

	resp := s.handler.Handle(RPCRequest{
		Method: method,
		Params: params,
	})

	w.Header().Set("Content-Type", "application/json")
	if resp.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": resp.Error})
		return
	}
	w.Write(resp.Result)
}

// Serve starts listening. Blocks until error or shutdown.
func (s *Server) Serve() error {
	s.logger.Printf("HTTP API listening on %s", s.httpSrv.Addr)
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

// handleHealth returns daemon status (no auth required).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}
