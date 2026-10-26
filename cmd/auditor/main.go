// Command auditor is TrackLin.es's audit service (TL-5): it wires the config, database and HTTP
// service together, then serves until stopped.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	bugfixes "github.com/bugfixes/go-bugfixes"
	"github.com/bugfixes/go-bugfixes/logs"

	service "github.com/tracklines/auditor/internal"
	"github.com/tracklines/auditor/internal/config"
)

func main() {
	if err := run(); err != nil {
		_ = logs.Errorf("auditor: %v", err)
		time.Sleep(bugfixes.DefaultTimeout) // the SDK reports in the background: let it finish before exiting
		os.Exit(1)
	}
}

func run() error {
	c, err := config.Build()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := c.Database.GetPGXPoolClient(ctx)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer db.Close()
	s := &service.Service{DB: db, DBAddr: net.JoinHostPort(c.Database.Host, strconv.Itoa(c.Database.Port)), Port: c.Port()}
	return s.Serve(ctx)
}
