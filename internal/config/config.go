// Package config reads fogbell's configuration file and checks it before anything starts.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
	_ "time/tzdata"

	"go.yaml.in/yaml/v3"

	"github.com/fogbell-dev/fogbell/internal/alert"
)

// Config is the whole configuration file. docs/configuration.md describes every field.
type Config struct {
	Listen       string       `yaml:"listen"`
	Slack        Slack        `yaml:"slack"`
	Alertmanager Alertmanager `yaml:"alertmanager"`
	Channels     Channels     `yaml:"channels"`
	Incidents    Incidents    `yaml:"incidents"`
	Objects      []Object     `yaml:"objects"`
	Links        Links        `yaml:"links"`
	Schedule     Schedule     `yaml:"schedule"`
	OnCall       OnCall       `yaml:"onCall"`
	Enrich       Enrich       `yaml:"enrich"`
	Tickets      Tickets      `yaml:"tickets"`

	location  *time.Location
	objects   alert.Objects
	templates map[string]*template.Template
	icons     []compiledIcon
}

// Slack is the Slack app's credentials and the metadata event types fogbell writes.
type Slack struct {
	TokenFile         string   `yaml:"tokenFile"`
	SigningSecretFile string   `yaml:"signingSecretFile"`
	APIURL            string   `yaml:"apiURL"`
	Events            Events   `yaml:"events"`
	AdoptEvents       []string `yaml:"adoptEvents"`
}

// Events names the metadata event types of fogbell's messages.
type Events struct {
	Incident    string `yaml:"incident"`
	Digest      string `yaml:"digest"`
	NoiseReport string `yaml:"noiseReport"`
}

// Alertmanager is where silences are made and firing alerts are read.
type Alertmanager struct {
	URL string `yaml:"url"`
}

// Channels says where fogbell posts and reports.
type Channels struct {
	Alerts          []string          `yaml:"alerts"`
	Digest          []string          `yaml:"digest"`
	NoiseReport     string            `yaml:"noiseReport"`
	SilenceOverview string            `yaml:"silenceOverview"`
	Teams           map[string]string `yaml:"teams"`
	TeamLabel       string            `yaml:"teamLabel"`
}

// Incidents tunes the incident lifecycle.
type Incidents struct {
	PageReceivers   []string `yaml:"pageReceivers"`
	ReopenWindow    Duration `yaml:"reopenWindow"`
	HistoryWindow   Duration `yaml:"historyWindow"`
	ShortLived      Duration `yaml:"shortLived"`
	NotObjectLabels []string `yaml:"notObjectLabels"`
	Icons           []Icon   `yaml:"icons"`
	QuietAlerts     []string `yaml:"quietAlerts"`
	Fold            Fold     `yaml:"fold"`
}

// Icon is the icon of a group whose common labels match.
type Icon struct {
	Match map[string]string `yaml:"match"`
	Icon  string            `yaml:"icon"`
}

type compiledIcon struct {
	match map[string]*regexp.Regexp
	icon  string
}

// Fold folds pod alerts on a node into the thread of that node's open incident.
type Fold struct {
	ParentAlertname string   `yaml:"parentAlertname"`
	Label           string   `yaml:"label"`
	Window          Duration `yaml:"window"`
}

// Object is one kind of broken object an alert can be about.
type Object struct {
	Name    string            `yaml:"name"`
	Labels  []string          `yaml:"labels"`
	Exclude map[string]string `yaml:"exclude"`
	Button  string            `yaml:"button"`
	URL     string            `yaml:"url"`
}

// Links are the link buttons that are not about one object.
type Links struct {
	AllAlerts string `yaml:"allAlerts"`
	Ask       string `yaml:"ask"`
	AskLabel  string `yaml:"askLabel"`
}

// Schedule is when the digest and the noise report are posted.
type Schedule struct {
	Timezone       string `yaml:"timezone"`
	DigestAt       string `yaml:"digestAt"`
	NoiseReportDay string `yaml:"noiseReportDay"`
}

// OnCall is the roster file escalations read and when the rotation hands over.
type OnCall struct {
	RosterFile string `yaml:"rosterFile"`
	HandoverAt string `yaml:"handoverAt"`
}

// Enrich configures the read-only lookups that add to an incident.
type Enrich struct {
	Metrics Metrics  `yaml:"metrics"`
	Ruler   Ruler    `yaml:"ruler"`
	Logs    Logs     `yaml:"logs"`
	Related bool     `yaml:"related"`
	Charts  bool     `yaml:"charts"`
	Flux    Flux     `yaml:"flux"`
	Refresh Duration `yaml:"refresh"`
}

// Metrics is a Prometheus-compatible query API.
type Metrics struct {
	URL string `yaml:"url"`
}

// Ruler is where a rule's expression is read.
type Ruler struct {
	URL           string `yaml:"url"`
	GeneratorHost string `yaml:"generatorHost"`
}

// Logs is a VictoriaLogs query API and the stream fields that name a pod.
type Logs struct {
	URL       string `yaml:"url"`
	Namespace string `yaml:"namespaceField"`
	Pod       string `yaml:"podField"`
	Container string `yaml:"containerField"`
}

// Flux reads Flux Kustomizations and HelmReleases for the change before an alert.
type Flux struct {
	Enabled    bool     `yaml:"enabled"`
	Window     Duration `yaml:"window"`
	CommitURL  string   `yaml:"commitURL"`
	PathURL    string   `yaml:"pathURL"`
	OwnSource  string   `yaml:"ownSource"`
	Kubeconfig string   `yaml:"kubeconfig"`
}

// Tickets is where the ticket button files a ticket.
type Tickets struct {
	ClickUp ClickUp `yaml:"clickup"`
}

// ClickUp files tickets in one list.
type ClickUp struct {
	TokenFile string `yaml:"tokenFile"`
	List      string `yaml:"list"`
}

// Duration is a time.Duration written as "90s", "30m", "1h" or "8d".
type Duration time.Duration

// UnmarshalYAML reads a Go duration, with "d" for days.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Seconds is the duration in seconds.
func (d Duration) Seconds() float64 { return time.Duration(d).Seconds() }

// ParseDuration reads a Go duration, with "d" for days.
func ParseDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err != nil {
			return 0, fmt.Errorf("duration %q: %w", s, err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// Load reads, defaults and checks a configuration file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse defaults and checks configuration bytes.
func Parse(raw []byte) (*Config, error) {
	c := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.defaults()
	if err := c.compile(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

func (c *Config) defaults() {
	set := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	setDur := func(p *Duration, v time.Duration) {
		if *p == 0 {
			*p = Duration(v)
		}
	}
	set(&c.Listen, ":8080")
	set(&c.Slack.APIURL, "https://slack.com/api/")
	set(&c.Slack.Events.Incident, "fogbell_incident")
	set(&c.Slack.Events.Digest, "fogbell_digest")
	set(&c.Slack.Events.NoiseReport, "fogbell_noise_report")
	set(&c.Channels.TeamLabel, "team")
	if c.Incidents.PageReceivers == nil {
		c.Incidents.PageReceivers = []string{"page"}
	}
	setDur(&c.Incidents.ReopenWindow, time.Hour)
	setDur(&c.Incidents.HistoryWindow, 8*24*time.Hour)
	setDur(&c.Incidents.ShortLived, 10*time.Minute)
	if c.Incidents.NotObjectLabels == nil {
		c.Incidents.NotObjectLabels = []string{"alertname", "severity", "alertgroup", "audience", "team", "cluster",
			"prometheus", "receiver", "endpoint", "service", "instance_type"}
	}
	if c.Incidents.QuietAlerts == nil {
		c.Incidents.QuietAlerts = []string{"Watchdog", "InfoInhibitor"}
	}
	set(&c.Incidents.Fold.ParentAlertname, "KubeNodeNotReady")
	set(&c.Incidents.Fold.Label, "node")
	setDur(&c.Incidents.Fold.Window, 30*time.Minute)
	if c.Objects == nil {
		c.Objects = []Object{
			{Name: "pod", Labels: []string{"namespace", "pod"}},
			{Name: "node", Labels: []string{"node"}},
		}
	}
	set(&c.Links.AskLabel, "Ask in chat")
	set(&c.Schedule.Timezone, "UTC")
	set(&c.Schedule.DigestAt, "09:00")
	set(&c.Schedule.NoiseReportDay, "Monday")
	set(&c.OnCall.HandoverAt, "09:00")
	set(&c.Enrich.Logs.Namespace, "k8s_namespace")
	set(&c.Enrich.Logs.Pod, "k8s_pod")
	set(&c.Enrich.Logs.Container, "k8s_container")
	setDur(&c.Enrich.Flux.Window, time.Hour)
	set(&c.Enrich.Flux.OwnSource, "flux-system")
	setDur(&c.Enrich.Refresh, 5*time.Minute)
}

func (c *Config) compile() error {
	var errs []error
	loc, err := time.LoadLocation(c.Schedule.Timezone)
	if err != nil {
		errs = append(errs, fmt.Errorf("schedule.timezone: %w", err))
	}
	c.location = loc
	if _, _, err := c.DigestClock(); err != nil {
		errs = append(errs, err)
	}
	if _, _, err := c.HandoverClock(); err != nil {
		errs = append(errs, err)
	}
	if _, err := c.NoiseWeekday(); err != nil {
		errs = append(errs, err)
	}
	if c.Alertmanager.URL == "" {
		errs = append(errs, errors.New("alertmanager.url is required: silences are made there"))
	}
	for name, u := range map[string]string{"alertmanager.url": c.Alertmanager.URL, "enrich.metrics.url": c.Enrich.Metrics.URL,
		"enrich.ruler.url": c.Enrich.Ruler.URL, "enrich.logs.url": c.Enrich.Logs.URL} {
		if u == "" {
			continue
		}
		if p, err := url.Parse(u); err != nil || p.Scheme == "" || p.Host == "" {
			errs = append(errs, fmt.Errorf("%s: %q is not an absolute URL", name, u))
		}
	}
	c.templates = map[string]*template.Template{}
	notObject := map[string]bool{}
	for _, l := range c.Incidents.NotObjectLabels {
		notObject[l] = true
	}
	c.objects = alert.Objects{NotObject: notObject}
	for i, o := range c.Objects {
		if o.Name == "" || len(o.Labels) == 0 {
			errs = append(errs, fmt.Errorf("objects[%d]: name and labels are required", i))
			continue
		}
		kind := alert.ObjectKind{Name: o.Name, Labels: o.Labels, Exclude: map[string]*regexp.Regexp{}}
		for l, expr := range o.Exclude {
			re, err := regexp.Compile(expr)
			if err != nil {
				errs = append(errs, fmt.Errorf("objects[%d].exclude.%s: %w", i, l, err))
				continue
			}
			kind.Exclude[l] = re
		}
		c.objects.Kinds = append(c.objects.Kinds, kind)
		if o.URL != "" {
			if err := c.addTemplate("object:"+o.Name, o.URL); err != nil {
				errs = append(errs, fmt.Errorf("objects[%d].url: %w", i, err))
			}
		}
	}
	if c.Links.Ask != "" {
		if err := c.addTemplate("ask", c.Links.Ask); err != nil {
			errs = append(errs, fmt.Errorf("links.ask: %w", err))
		}
	}
	for i, ic := range c.Incidents.Icons {
		ci := compiledIcon{icon: ic.Icon, match: map[string]*regexp.Regexp{}}
		for l, expr := range ic.Match {
			re, err := regexp.Compile("^(?:" + expr + ")$")
			if err != nil {
				errs = append(errs, fmt.Errorf("incidents.icons[%d].match.%s: %w", i, l, err))
				continue
			}
			ci.match[l] = re
		}
		c.icons = append(c.icons, ci)
	}
	if c.Enrich.Ruler.GeneratorHost != "" {
		if _, err := regexp.Compile(c.Enrich.Ruler.GeneratorHost); err != nil {
			errs = append(errs, fmt.Errorf("enrich.ruler.generatorHost: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (c *Config) addTemplate(name, text string) error {
	t, err := template.New(name).Option("missingkey=zero").Funcs(Funcs).Parse(text)
	if err != nil {
		return err
	}
	c.templates[name] = t
	return nil
}

// Funcs are the functions a link template can call.
var Funcs = template.FuncMap{
	"queryescape": url.QueryEscape,
	"pathescape":  url.PathEscape,
	"quote": func(s string) string {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	},
}

// Location is the time zone of the schedule and the roster.
func (c *Config) Location() *time.Location { return c.location }

// ObjectReader is the compiled object kinds.
func (c *Config) ObjectReader() *alert.Objects { return &c.objects }

// Render executes a link template; "" when there is none or it fails.
func (c *Config) Render(name string, data any) (string, error) {
	t := c.templates[name]
	if t == nil {
		return "", nil
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

// HasTemplate reports whether a link template is configured.
func (c *Config) HasTemplate(name string) bool { return c.templates[name] != nil }

// ObjectButton is the button label of an object kind.
func (c *Config) ObjectButton(kind string) string {
	for _, o := range c.Objects {
		if o.Name == kind {
			if o.Button != "" {
				return o.Button
			}
			return "Open"
		}
	}
	return "Open"
}

// Icon is the configured icon of a group whose common labels match a rule; "" when none does.
func (c *Config) Icon(labels map[string]string) string {
	for _, ic := range c.icons {
		ok := len(ic.match) > 0
		for l, re := range ic.match {
			if !re.MatchString(labels[l]) {
				ok = false
			}
		}
		if ok {
			return ic.icon
		}
	}
	return ""
}

// DigestClock is the hour and minute of the daily digest.
func (c *Config) DigestClock() (int, int, error) {
	return clock("schedule.digestAt", c.Schedule.DigestAt)
}

// HandoverClock is the hour and minute the on-call rotation hands over.
func (c *Config) HandoverClock() (int, int, error) {
	return clock("onCall.handoverAt", c.OnCall.HandoverAt)
}

func clock(field, value string) (int, int, error) {
	var h, m int
	if _, err := fmt.Sscanf(value, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("%s: %q is not HH:MM", field, value)
	}
	return h, m, nil
}

// NoiseWeekday is the day of the weekly noise report.
func (c *Config) NoiseWeekday() (time.Weekday, error) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		if strings.EqualFold(d.String(), c.Schedule.NoiseReportDay) {
			return d, nil
		}
	}
	return 0, fmt.Errorf("schedule.noiseReportDay: %q is not a day of the week", c.Schedule.NoiseReportDay)
}

// IsPage reports whether a receiver pages: its first post mentions the channel and it escalates.
func (c *Config) IsPage(receiver string) bool {
	for _, r := range c.Incidents.PageReceivers {
		if r == receiver {
			return true
		}
	}
	return false
}

// Quiet reports whether an alert always fires by design and is never worth naming as a neighbour.
func (c *Config) Quiet(alertname string) bool {
	for _, q := range c.Incidents.QuietAlerts {
		if q == alertname {
			return true
		}
	}
	return false
}

// TeamChannel is a team's own channel, "" when it has none.
func (c *Config) TeamChannel(team string) string {
	return strings.TrimPrefix(c.Channels.Teams[team], "#")
}

// AlertChannels is every channel incidents are posted in, team channels included.
func (c *Config) AlertChannels() []string { return withTeams(c.Channels.Alerts, c.Channels.Teams) }

// DigestChannels is every channel that gets the digest, team channels included.
func (c *Config) DigestChannels() []string { return withTeams(c.Channels.Digest, c.Channels.Teams) }

func withTeams(base []string, teams map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(ch string) {
		ch = strings.TrimPrefix(ch, "#")
		if ch != "" && !seen[ch] {
			seen[ch] = true
			out = append(out, ch)
		}
	}
	for _, ch := range base {
		add(ch)
	}
	names := make([]string, 0, len(teams))
	for _, ch := range teams {
		names = append(names, strings.TrimPrefix(ch, "#"))
	}
	sort.Strings(names)
	for _, ch := range names {
		add(ch)
	}
	return out
}
