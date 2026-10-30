package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPloegAdmitsAWorkItemAndReadsItsFindings(t *testing.T) {
	var admitted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer op-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/operator/work-items":
			_ = json.NewDecoder(r.Body).Decode(&admitted)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"schemaVersion":"1.0","workItem":{"id":"77","team":"sre","state":"queued","shiftId":null,"budgetUsd":2,"poolUsd":null},"created":true,"replayed":false}`)
		case r.URL.Path == "/api/v1/operator/work-items/77/facts":
			_, _ = io.WriteString(w, `{"workItem":{"state":"done","url":""},"runs":[
				{"role":"investigate","summary":"First look.","problem":"","solution":""},
				{"role":"investigate","summary":"The disk filled with logs.","problem":"Log rotation is off.","solution":"Turn it on."}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("op-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Ploeg{URL: srv.URL, TokenFile: token, Team: "sre", Target: "cluster", BudgetUSD: 2, Console: "https://unfold.example/work", HTTP: srv.Client()}
	if !p.Available() {
		t.Fatal("not available with a token")
	}
	id, err := p.Start(context.Background(), Request{ID: `{}:{alertname="X"}@1793089000`, Title: "Investigate: X", Description: "brief"})
	if err != nil || id != "77" {
		t.Fatal(id, err)
	}
	if rid := admitted["requestId"].(string); !strings.HasPrefix(rid, "fogbell:") || strings.ContainsAny(rid, `{}"=@`) {
		t.Fatal(rid)
	}
	if admitted["team"] != "sre" || admitted["target"] != "cluster" || admitted["budgetUsd"].(float64) != 2 {
		t.Fatal(admitted)
	}
	if _, has := admitted["model"]; has {
		t.Fatal("an empty model is sent; Ploeg refuses unknown and empty fields")
	}
	s, err := p.Status(context.Background(), "77")
	if err != nil || !s.Done || s.URL != "https://unfold.example/work/77" {
		t.Fatal(s, err)
	}
	if s.Findings != "The disk filled with logs.\n*Cause:* Log rotation is off.\n*Fix:* Turn it on." {
		t.Fatal(s.Findings)
	}
}

func TestPloegRefusalIsErrRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusPaymentRequired) }))
	defer srv.Close()
	p := &Ploeg{URL: srv.URL, TokenFile: "/nonexistent", Team: "t", Target: "x", HTTP: srv.Client()}
	if p.Available() {
		t.Fatal("available without a token file")
	}
	if _, err := p.Start(context.Background(), Request{ID: "a", Title: "t", Description: "d"}); !errors.Is(err, ErrRefused) {
		t.Fatal(err)
	}
}
