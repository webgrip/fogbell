// Command fogbell turns Alertmanager notifications into one Slack thread per incident.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/webgrip/fogbell/internal/app"
	"github.com/webgrip/fogbell/internal/config"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fogbell:", err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("config", envOr("FOGBELL_CONFIG", "/etc/fogbell/config.yaml"), "configuration file")
	check := flag.Bool("check", false, "check the configuration and exit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *check {
		fmt.Println("configuration ok")
		return nil
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("version", version)
	slog.SetDefault(log)
	a, err := app.Build(cfg, log, version)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.Run(ctx)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
