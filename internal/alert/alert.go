// Package alert reads Alertmanager webhook notifications: their alerts, the broken object each one is
// about, and the fields of the alert contract.
package alert

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Group is one Alertmanager webhook notification (version 4).
type Group struct {
	Receiver          string            `json:"receiver"`
	Status            string            `json:"status"`
	Alerts            []Alert           `json:"alerts"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	GroupKey          string            `json:"groupKey"`
}

// Alert is one alert of a notification.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Resolved reports whether the notification is a resolve.
func (g *Group) Resolved() bool { return g.Status == "resolved" }

// Key is the group key, or the group labels when Alertmanager sent none.
func (g *Group) Key() string {
	if g.GroupKey != "" {
		return g.GroupKey
	}
	return SortedJSON(g.GroupLabels)
}

// Label is a common label, "" when absent.
func (g *Group) Label(name string) string { return g.CommonLabels[name] }

// Active is the alerts in the group's own status, or all of them when none is.
func (g *Group) Active() []Alert {
	status := g.Status
	if status == "" {
		status = "firing"
	}
	var out []Alert
	for _, a := range g.Alerts {
		if a.Status == status {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return g.Alerts
	}
	return out
}

// First is the first active alert, or an empty one.
func (g *Group) First() Alert {
	if a := g.Active(); len(a) > 0 {
		return a[0]
	}
	return Alert{}
}

// Headline is the first alert's summary, else the alert name, else "Alert".
func (g *Group) Headline() string {
	if len(g.Alerts) > 0 && g.Alerts[0].Annotations["summary"] != "" {
		return g.Alerts[0].Annotations["summary"]
	}
	if n := g.Label("alertname"); n != "" {
		return n
	}
	return "Alert"
}

// Start is when the alert started, in Unix seconds; 0 when unknown.
func (a Alert) Start() float64 { return ParseTime(a.StartsAt) }

// End is when the alert ended, in Unix seconds; 0 while it fires.
func (a Alert) End() float64 { return ParseTime(a.EndsAt) }

// ParseTime reads an Alertmanager timestamp; the zero time reads as 0.
func ParseTime(value string) float64 {
	if value == "" || strings.HasPrefix(value, "0001-") {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return float64(t.UnixNano()) / 1e9
}

// Contract is the alert contract's annotation names, in the order a ticket or postmortem uses them.
var Contract = []string{"summary", "subject", "description", "impact", "action", "runbook_url", "dashboard_url"}

// ObjectKind says how an alert names the object that is broken: a pod by its namespace and pod, a
// node by its node.
type ObjectKind struct {
	Name    string
	Labels  []string
	Exclude map[string]*regexp.Regexp
}

// Objects reads the broken object of an alert from its labels, kind by kind in order.
type Objects struct {
	Kinds []ObjectKind
	// NotObject names labels that describe the rule or the route, never the object.
	NotObject map[string]bool
}

// Match is the first kind whose labels an alert carries, with those labels; nil when none.
func (o *Objects) Match(labels map[string]string) (*ObjectKind, map[string]string) {
	for i := range o.Kinds {
		k := &o.Kinds[i]
		got := map[string]string{}
		ok := true
		for _, l := range k.Labels {
			if labels[l] == "" {
				ok = false
				break
			}
			got[l] = labels[l]
		}
		if !ok {
			continue
		}
		for l, re := range k.Exclude {
			if re.MatchString(labels[l]) {
				ok = false
			}
		}
		if ok {
			return k, got
		}
	}
	return nil, nil
}

// Is reports whether an alert's object is of the named kind.
func (o *Objects) Is(labels map[string]string, kind string) bool {
	k, _ := o.Match(labels)
	return k != nil && k.Name == kind
}

// Subject names the broken object: the subject annotation, else the matched kind's label values
// joined by a slash.
func (o *Objects) Subject(a Alert) string {
	if s := a.Annotations["subject"]; s != "" {
		return s
	}
	k, got := o.Match(a.Labels)
	if k == nil {
		return ""
	}
	parts := make([]string, len(k.Labels))
	for i, l := range k.Labels {
		parts[i] = got[l]
	}
	return strings.Join(parts, "/")
}

// Labels names the object for a silence on it alone. A subject annotation is free text, so there
// the object is every label whose whole value it names.
func (o *Objects) Labels(a Alert) map[string]string {
	if s := a.Annotations["subject"]; s != "" {
		named := map[string]string{}
		for k, v := range a.Labels {
			if o.NotObject[k] || len([]rune(v)) <= 1 {
				continue
			}
			if namesWhole(s, v) {
				named[k] = v
			}
		}
		if len(named) > 0 {
			return named
		}
	}
	_, got := o.Match(a.Labels)
	return got
}

func namesWhole(text, value string) bool {
	word := func(r byte) bool {
		return r == '_' || r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
	}
	for from := 0; ; {
		i := strings.Index(text[from:], value)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(value)
		if (start == 0 || !word(text[start-1])) && (end == len(text) || !word(text[end])) {
			return true
		}
		from = start + 1
	}
}

// Object is a distinct broken object of a group, for a silence narrowed to it.
type Object struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

// Distinct is the distinct objects of a group's active alerts, at most limit.
func (o *Objects) Distinct(g *Group, limit int) []Object {
	out := []Object{}
	for _, a := range g.Active() {
		labels := o.Labels(a)
		if len(labels) > 0 && !containsLabels(out, labels) {
			name := o.Subject(a)
			if name == "" {
				name = strings.Join(sortedValues(labels), ",")
			}
			out = append(out, Object{Name: name, Labels: labels})
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

func containsLabels(objs []Object, labels map[string]string) bool {
	for _, o := range objs {
		if equal(o.Labels, labels) {
			return true
		}
	}
	return false
}

func equal(a, b map[string]string) bool {
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

// ContractFields is the first active alert's contract annotations, with the subject as Subject reads it.
func (o *Objects) ContractFields(g *Group) map[string]string {
	active := g.Active()
	if len(active) == 0 {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, k := range Contract {
		if v := active[0].Annotations[k]; v != "" {
			out[k] = v
		}
	}
	if s := o.Subject(active[0]); s != "" {
		out["subject"] = s
	}
	return out
}

func sortedValues(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out
}

// SortedJSON encodes labels as JSON with sorted keys.
func SortedJSON(m map[string]string) string {
	if m == nil {
		m = map[string]string{}
	}
	b, _ := json.Marshal(m)
	return string(b)
}
