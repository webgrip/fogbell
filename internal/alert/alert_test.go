package alert

import (
	"regexp"
	"testing"
)

func objects() *Objects {
	return &Objects{
		Kinds: []ObjectKind{
			{Name: "pod", Labels: []string{"namespace", "pod"}, Exclude: map[string]*regexp.Regexp{"pod": regexp.MustCompile(`^(kube-state-metrics|node-exporter)-`)}},
			{Name: "node", Labels: []string{"node"}},
		},
		NotObject: map[string]bool{"alertname": true, "severity": true, "team": true},
	}
}

func eq(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestObjectLabelsFollowTheSubject(t *testing.T) {
	o := objects()
	for _, c := range []struct {
		alert Alert
		want  map[string]string
	}{
		{Alert{Labels: map[string]string{"alertname": "A", "namespace": "ns", "pod": "web-1"}}, map[string]string{"namespace": "ns", "pod": "web-1"}},
		{Alert{Labels: map[string]string{"namespace": "observability", "pod": "kube-state-metrics-0", "node": "w1"}}, map[string]string{"node": "w1"}},
		{Alert{Labels: map[string]string{"alertname": "ReplicasMismatch", "namespace": "observability", "deployment": "ci-exporter", "severity": "warning", "team": "ops"},
			Annotations: map[string]string{"subject": "observability/ci-exporter"}}, map[string]string{"namespace": "observability", "deployment": "ci-exporter"}},
		{Alert{Labels: map[string]string{"job": "ci"}, Annotations: map[string]string{"subject": "ci-exporter"}}, nil},
		{Alert{Labels: map[string]string{"alertname": "A"}}, nil},
	} {
		if got := o.Labels(c.alert); !eq(got, c.want) {
			t.Errorf("Labels(%v) = %v, want %v", c.alert.Labels, got, c.want)
		}
	}
}

func TestTwoAlertsOnOnePodAreOneObject(t *testing.T) {
	g := &Group{Status: "firing", Alerts: []Alert{
		{Status: "firing", Labels: map[string]string{"namespace": "ns", "pod": "p0"}},
		{Status: "firing", Labels: map[string]string{"namespace": "ns", "pod": "p0", "container": "sidecar"}},
	}}
	got := objects().Distinct(g, 5)
	if len(got) != 1 || got[0].Name != "ns/p0" {
		t.Fatal(got)
	}
}

func TestParseTime(t *testing.T) {
	if ParseTime("0001-01-01T00:00:00Z") != 0 || ParseTime("") != 0 {
		t.Fatal("zero time")
	}
	if got := ParseTime("2026-10-27T08:15:00.123456789Z"); got < 1793088900.12 || got > 1793088900.13 {
		t.Fatal(got)
	}
}
