// Command auditor is TrackLin.es's audit service (TL-5): it wires the config, database and HTTP
// service together, then serves until stopped.
package main

import (
	"context"
	"fmt"
	"log"
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
	// progress goes to stdout with the standard logger: the Bugfixes SDK only prints at its log level
	// (error, by default) once it has agent keys, so its Logf lines never reach Railway's logs
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.Print("auditor: starting")
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
	log.Printf("auditor: postgres at %s:%d", c.Database.Host, c.Database.Port)
	s := &service.Service{DB: db, DBAddr: net.JoinHostPort(c.Database.Host, strconv.Itoa(c.Database.Port)), Port: c.Port()}
	return s.Serve(ctx)
}
