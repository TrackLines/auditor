// Package service is the auditor's HTTP API (TL-5): an append-only record of what happened in each
// TrackLin.es organization, kept in its own database so the backend can't change history. Only the
// backend calls it, over Railway's private network; it has no public domain.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/bugfixes/go-bugfixes/logs"
	"github.com/jackc/pgx/v5/pgxpool"
	healthcheck "github.com/keloran/go-healthcheck"
)

type Service struct {
	DB     *pgxpool.Pool
	DBAddr string // host:port, for the health check's own probe
	Port   string
}

// Handler declares every route.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	return mux
}

// health: healthy only when Postgres is, checked by go-healthcheck's probe and by the pool itself
// (the probe doesn't log in); 503 otherwise, so Railway holds back a deploy that can't reach it. The
// body says pass or fail only, never internal addresses.
func (s *Service) health(w http.ResponseWriter, r *http.Request) {
	status, code := healthcheck.HealthPass, http.StatusOK
	if err := s.checkHealth(r.Context()); err != nil {
		if r.Context().Err() == nil {
			logs.Warnf("health: %v", err)
		}
		status, code = healthcheck.HealthFail, http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/health+json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(healthcheck.Health{Name: "auditor", Status: status})
}

func (s *Service) checkHealth(ctx context.Context) error {
	deps, _ := json.Marshal(healthcheck.Dependencies{Dependencies: []healthcheck.Dependency{{Name: "postgres", URL: s.DBAddr, Type: "postgres"}}})
	h, err := healthcheck.HealthCheck{Name: "auditor", Dependencies: string(deps)}.Check()
	if err != nil {
		return err
	}
	if h.Status != healthcheck.HealthPass {
		return errors.New("postgres probe failed")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.DB.Ping(ctx)
}

// Serve runs until ctx ends (SIGINT/SIGTERM), then lets in-flight requests finish before returning,
// so main closes the database only after them.
func (s *Service) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", ":"+s.Port)
	if err != nil {
		return err
	}
	logs.Local().Logf("auditor: listening on %s", ln.Addr())
	return serveOn(ctx, ln, s.Handler(), 15*time.Second)
}

func serveOn(ctx context.Context, ln net.Listener, h http.Handler, drain time.Duration) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	err := srv.Shutdown(shutdown)
	<-served
	return err
}
