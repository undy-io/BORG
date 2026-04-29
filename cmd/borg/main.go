package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/undy-io/BORG/internal/app"
	"github.com/undy-io/BORG/internal/config"
)

const defaultShutdownTimeout = 30 * time.Second

type serverRunner struct {
	listenAndServe func() error
	shutdown       func(context.Context) error
	close          func() error
	timeout        time.Duration
}

func main() {
	var configPathFlag string
	var hostFlag string
	var portFlag string
	var reloadFlag bool

	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flags.StringVar(&configPathFlag, "config", "", "Path to YAML/JSON file containing config.")
	flags.StringVar(&configPathFlag, "c", "", "Path to YAML/JSON file containing config.")
	flags.StringVar(&hostFlag, "host", "", "Bind address (default: 0.0.0.0)")
	flags.StringVar(&portFlag, "port", "", "Port to bind (default: PORT env var or 8000)")
	flags.BoolVar(&reloadFlag, "reload", false, "Accepted for Python CLI compatibility; no-op in Go.")

	if err := flags.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
	_ = reloadFlag

	configPath := config.ResolveConfigPath(configPathFlag)
	if err := os.Setenv(config.ProxyConfigEnv, configPath); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	port, err := config.ResolvePort(portFlag)
	if err != nil {
		log.Fatal(err)
	}
	host := config.ResolveHost(hostFlag)

	borgApp, err := app.New(configPath)
	if err != nil {
		log.Fatal(err)
	}
	defer borgApp.Close()

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	server := &http.Server{
		Addr:              addr,
		Handler:           borgApp.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("BORG Go proxy listening on %s", fmt.Sprintf("http://%s", addr))
	if err := runHTTPServer(ctx, serverRunner{
		listenAndServe: server.ListenAndServe,
		shutdown:       server.Shutdown,
		close:          server.Close,
		timeout:        defaultShutdownTimeout,
	}); err != nil {
		log.Fatal(err)
	}
}

func runHTTPServer(ctx context.Context, runner serverRunner) error {
	if runner.timeout <= 0 {
		runner.timeout = defaultShutdownTimeout
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- runner.listenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		log.Printf("BORG Go proxy shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), runner.timeout)
		defer cancel()

		if err := runner.shutdown(shutdownCtx); err != nil {
			log.Printf("BORG Go proxy graceful shutdown failed: %v", err)
			if closeErr := runner.close(); closeErr != nil {
				log.Printf("BORG Go proxy close failed: %v", closeErr)
				return errors.Join(err, closeErr)
			}
			return err
		}

		err := <-serverErr
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
