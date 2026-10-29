// Command fogbell turns Alertmanager notifications into one Slack thread per incident.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/webgrip/fogbell/internal/config"
	"github.com/webgrip/fogbell/internal/engine"
	"github.com/webgrip/fogbell/internal/metrics"
	"github.com/webgrip/fogbell/internal/server"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/upstream"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fogbell:", err)
		os.Exit(1)
	}
}

func readFile(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
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
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	token := readFile(cfg.Slack.TokenFile)
	if token == "" {
		return errors.New("slack.tokenFile is empty or unreadable: fogbell posts with a bot token")
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	m := metrics.New()
	sl := slack.New(token, httpClient)
	sl.BaseURL = cfg.Slack.APIURL
	sl.Log = log
	sl.Record = func(method, result string) { m.SlackRequests.WithLabelValues(method, result).Inc() }

	am := &upstream.Alertmanager{URL: cfg.Alertmanager.URL, HTTP: httpClient}
	e := engine.New(cfg, sl, am, m)
	e.Log = log
	signing := func() string { return readFile(cfg.Slack.SigningSecretFile) }
	e.Interactive = func() bool { return signing() != "" }
	if u := cfg.Enrich.Metrics.URL; u != "" {
		e.Prom = &upstream.Prometheus{URL: u, HTTP: httpClient}
	}
	if u := cfg.Enrich.Ruler.URL; u != "" || e.Prom != nil {
		r := &upstream.Ruler{URL: u, HTTP: httpClient}
		if h := cfg.Enrich.Ruler.GeneratorHost; h != "" {
			r.GeneratorHost = regexp.MustCompile(h)
		}
		e.Ruler = r
	}
	if u := cfg.Enrich.Logs.URL; u != "" {
		e.Logs = &upstream.Logs{URL: u, HTTP: httpClient}
	}
	if cfg.Enrich.Flux.Enabled {
		kube, err := upstream.InCluster()
		switch {
		case err != nil:
			log.Warn("no Kubernetes API; no change line", "error", err)
		case kube == nil:
			log.Warn("not in a cluster; no change line")
		default:
			e.Kube = kube
		}
	}
	if cu := cfg.Tickets.ClickUp; cu.List != "" {
		e.Tracker = &upstream.ClickUp{TokenFile: cu.TokenFile, List: cu.List, HTTP: httpClient}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go every(ctx, 2*time.Minute, func(now time.Time) {
		for _, d := range e.Maintain(ctx) {
			log.Info("maintain", "result", d)
		}
		for _, d := range e.Tick(ctx, now) {
			log.Info("schedule", "result", d)
		}
	})

	srv := &server.Server{Engine: e, SigningSecret: signing, Log: log, Background: func(f func()) { go f() }}
	hs := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	log.Info("fogbell started", "version", version, "listen", cfg.Listen, "interactive", e.Interactive(),
		"alert_channels", cfg.AlertChannels(), "digest_channels", cfg.DigestChannels())
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func every(ctx context.Context, d time.Duration, f func(time.Time)) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			f(now)
		}
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
