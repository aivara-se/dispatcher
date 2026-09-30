// Command dispatcher is the service GitHub calls: it verifies a delivery,
// decides which single bot it belongs to, and wakes that bot with the reason
// attached. One event, one wake, one bot.
//
// The process is one binary that takes a routes file and listens on a loopback
// port; TLS terminates in front of it, and the wake goes out to a bot's gateway
// route. Both interfaces are in docs/SYSTEMS.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aivara-se/dispatcher/internal/audit"
	"github.com/aivara-se/dispatcher/internal/board"
	"github.com/aivara-se/dispatcher/internal/config"
	"github.com/aivara-se/dispatcher/internal/receiver"
	"github.com/aivara-se/dispatcher/internal/router"
	"github.com/aivara-se/dispatcher/internal/wake"
)

// shutdownGrace is how long a request in flight is given after a signal.
const shutdownGrace = 10 * time.Second

func main() {
	configPath := flag.String("config", "config/routes.yaml", "path to the routes file")
	flag.Parse()

	logger := log.New(os.Stderr, "dispatcher: ", log.LstdFlags|log.LUTC)
	if err := run(*configPath, logger); err != nil {
		logger.Printf("%v", err)
		os.Exit(1)
	}
}

// run loads the configuration, builds the components, serves until a signal,
// and shuts down cleanly. A configuration fault is refused here, at boot, and
// never at the first event (docs/adrs/006-configuration-and-secrets.md).
func run(configPath string, logger *log.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	trail, err := audit.Open(cfg.LogPath, audit.DefaultRotateBytes)
	if err != nil {
		return err
	}
	defer trail.Close()

	dead, err := audit.OpenDeadLetter(cfg.DeadLetterPath)
	if err != nil {
		return err
	}
	defer dead.Close()

	// The board read is where a routing decision leaves the process: it is
	// built here with the token the routes file names, so a reference that
	// resolves to nothing refuses at boot rather than at the first silent event
	// (docs/SYSTEMS.md sections 4 and 8). The components are built once, in
	// this order, and `main` is not the file that changes as the packages
	// below it grow.
	client := &http.Client{Timeout: cfg.RequestTimeout}
	read, err := board.New(cfg, client)
	if err != nil {
		return err
	}
	rt := router.New(cfg, read)
	poster := wake.New(cfg, client)
	rec := receiver.New(cfg, rt, poster, trail, dead)

	server := &http.Server{
		Addr:              cfg.Listen.Addr(),
		Handler:           rec.Handler(),
		ReadHeaderTimeout: cfg.RequestTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	served := make(chan error, 1)
	go func() { served <- server.ListenAndServe() }()
	logger.Printf("listening on %s, serving %s, %d route(s), log %s", cfg.Listen.Addr(), cfg.EndpointPath, len(cfg.Routes), cfg.LogPath)

	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serving %s: %w", cfg.Listen.Addr(), err)
	case <-ctx.Done():
		logger.Printf("signal received, shutting down")
		shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
