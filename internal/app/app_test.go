package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webgrip/fogbell/internal/config"
)

func TestBuildWiresWhatTheConfigurationTurnsOn(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("xoxb-t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(`
alertmanager: {url: http://am.example}
slack: {tokenFile: ` + token + `}
enrich: {metrics: {url: http://vm.example}, related: true, charts: true}
investigate: {ploeg: {url: http://ploeg.example, tokenFile: /none, team: sre, target: cluster}}
events: {webhooks: [{url: http://hook.example}]}
tickets: {clickup: {list: "9"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	a, err := Build(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	if a.Engine.Investigator == nil || a.Engine.Tracker == nil {
		t.Fatal("integrations not wired")
	}
	srv := httptest.NewServer(a.Handler)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(resp, err)
	}
	_ = resp.Body.Close()
}

func TestBuildNeedsAToken(t *testing.T) {
	cfg, _ := config.Parse([]byte("alertmanager: {url: http://am}\n"))
	if _, err := Build(cfg, slog.Default(), "test"); err == nil || !strings.Contains(err.Error(), "tokenFile") {
		t.Fatal(err)
	}
}
