package service

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tracklines/auditor/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Run(m)) }

func pool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	db, err := pgxpool.New(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	return db, u.Host
}

func health(s *Service) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	return rec
}

// healthy only when Postgres is: the probe and the pool; the body never names an address.
func TestHealthFollowsPostgres(t *testing.T) {
	db, addr := pool(t)
	ok := health(&Service{DB: db, DBAddr: addr})
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"status":"pass"`) {
		t.Fatalf("healthy: %d %s", ok.Code, ok.Body)
	}
	if strings.Contains(ok.Body.String(), addr) {
		t.Fatalf("leaks the database address: %s", ok.Body)
	}

	// nothing listening there: the probe fails
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	gone := ln.Addr().String()
	ln.Close()
	if rec := health(&Service{DB: db, DBAddr: gone}); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"status":"fail"`) {
		t.Fatalf("server down: %d %s", rec.Code, rec.Body)
	}

	// the server answers but our pool can't use it
	db.Close()
	if rec := health(&Service{DB: db, DBAddr: addr}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("pool closed: %d %s", rec.Code, rec.Body)
	}
}

// stopping waits for a request already running, then returns.
func TestServeDrainsOnStop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("done"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- serveOn(ctx, ln, h, 5*time.Second) }()
	got := make(chan int, 1)
	go func() {
		res, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			got <- 0
			return
		}
		res.Body.Close()
		got <- res.StatusCode
	}()
	<-started
	cancel()
	if code := <-got; code != http.StatusOK {
		t.Fatalf("in-flight request cut off: %d", code)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}
