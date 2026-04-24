package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/undy-io/BORG/internal/app"
	"github.com/undy-io/BORG/internal/config"
)

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
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
