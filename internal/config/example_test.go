package config

import (
	"os"
	"strings"
	"testing"
)

func TestTheExampleParsesAndItsTemplatesRender(t *testing.T) {
	raw, err := os.ReadFile("../../examples/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Render("object:pod", map[string]any{"Labels": map[string]string{"namespace": "shop", "pod": "web-1"}, "FromMs": int64(1)})
	if err != nil || !strings.HasPrefix(out, "https://grafana.example.com/explore?left=") || !strings.Contains(out, "web-1") {
		t.Fatal(out, err)
	}
}
