package redact

import (
	"strings"
	"testing"
)

func TestLine(t *testing.T) {
	for in, leak := range map[string]string{
		"connect db: password=hunter22":              "hunter22",
		"Authorization: Bearer abcdefghijklmnop":     "abcdefghijklmnop",
		"url https://user:s3cr3t@host/path":          "s3cr3t",
		`{"api_key": "k-123456789"}`:                 "k-123456789",
		"token glpat-abcdefghijklmnopqrst":           "abcdefghijklmnopqrst",
		"slack xoxb-1234567890-abcdefghij":           "abcdefghij",
		"jwt eyJhbGciOiJI.eyJzdWIiOiIx.c2lnbmF0dXJl": "c2lnbmF0dXJl",
	} {
		if out := Line(in); strings.Contains(out, leak) {
			t.Errorf("Line(%q) = %q still holds %q", in, out, leak)
		}
	}
	if got := Line("nothing to hide here"); got != "nothing to hide here" {
		t.Fatal(got)
	}
}
