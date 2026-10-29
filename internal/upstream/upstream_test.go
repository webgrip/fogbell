package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestClickUpMemberAndCreate(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "pk_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/team":
			_, _ = io.WriteString(w, `{"teams": [{"members": [{"user": {"id": 1, "email": "a@x"}}]}, {"members": [{"user": {"id": 7, "email": "Pat@Example.com"}}, {}]}]}`)
		case "/list/L1/task":
			_ = json.NewDecoder(r.Body).Decode(&sent)
			_, _ = io.WriteString(w, `{"id": "t", "url": "https://tracker.example/t/t"}`)
		}
	}))
	defer srv.Close()
	token := filepath.Join(t.TempDir(), "token")
	c := &ClickUp{API: srv.URL, TokenFile: token, List: "L1", HTTP: srv.Client()}
	if c.Available() {
		t.Fatal("available without a token file")
	}
	if err := os.WriteFile(token, []byte("pk_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !c.Available() {
		t.Fatal("the token arrived after start-up")
	}
	if id, err := c.Member(context.Background(), "pat@example.com"); err != nil || id != "7" {
		t.Fatal(id, err)
	}
	if id, _ := c.Member(context.Background(), "nobody@x"); id != "" {
		t.Fatal(id)
	}
	if _, err := c.Create(context.Background(), "n", "m", "7"); err != nil {
		t.Fatal(err)
	}
	if a, ok := sent["assignees"].([]any); !ok || a[0].(float64) != 7 {
		t.Fatal(sent)
	}
}

func TestRulerReadsBothGeneratorShapes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("group_id") != "11" || r.URL.Query().Get("alert_id") != "22" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"expression": "up == 0"}`)
	}))
	defer srv.Close()
	r := &Ruler{URL: srv.URL, HTTP: srv.Client(), GeneratorHost: regexp.MustCompile(`^vmalert-main-`)}
	ctx := context.Background()
	for gen, want := range map[string]string{
		"http://vmalert-main-abc:8080/vmalert/alert?group_id=11&alert_id=22":  "up == 0",
		"http://vmalert-audit-abc:8080/vmalert/alert?group_id=11&alert_id=22": "",
		"http://prometheus:9090/graph?g0.expr=rate%28x%5B5m%5D%29+%3E+1":      "rate(x[5m]) > 1",
		"": "",
	} {
		if got, err := r.Expression(ctx, gen); err != nil || got != want {
			t.Errorf("Expression(%q) = %q, %v", gen, got, err)
		}
	}
}

func TestPickAgreesOnSharedLabels(t *testing.T) {
	result := []Series{{Metric: map[string]string{"namespace": "other"}}, {Metric: map[string]string{"namespace": "ns", "__name__": "x"}}}
	if got := Pick(result, map[string]string{"namespace": "ns", "pod": "p"}); got == nil || got.Metric["namespace"] != "ns" {
		t.Fatal(got)
	}
	if got := Pick(result, map[string]string{"namespace": "third"}); got != nil {
		t.Fatal("a disagreeing series was picked from several")
	}
	if got := Pick(result[:1], map[string]string{"namespace": "ns"}); got == nil {
		t.Fatal("a single series is drawn even when it disagrees: the rule has only that one")
	}
	if got := Pick(result[:1], map[string]string{"pod": "p"}); got == nil {
		t.Fatal("one series and no shared label is that series")
	}
}
