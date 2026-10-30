package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c, err := Parse([]byte("alertmanager: {url: http://am:9093}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Slack.Events.Incident != "fogbell_incident" || !c.IsPage("page") || c.Location() != time.UTC {
		t.Fatal(c.Slack.Events, c.Location())
	}
	if time.Duration(c.Incidents.ReopenWindow) != time.Hour || time.Duration(c.Incidents.HistoryWindow) != 8*24*time.Hour {
		t.Fatal(c.Incidents)
	}
	if len(c.ObjectReader().Kinds) != 2 || !c.Quiet("Watchdog") {
		t.Fatal(c.ObjectReader().Kinds)
	}
}

func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	_, err := Parse([]byte(`
alertmanager: {url: "not a url"}
schedule: {timezone: Mars/Olympus, digestAt: "25:00", noiseReportDay: Someday}
objects: [{name: pod}]
links: {ask: "{{ .Prompt"}
`))
	if err == nil {
		t.Fatal("accepted a broken file")
	}
	for _, want := range []string{"alertmanager.url", "schedule.timezone", "schedule.digestAt", "schedule.noiseReportDay", "objects[0]", "links.ask"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

func TestUnknownFieldsAreRefused(t *testing.T) {
	if _, err := Parse([]byte("alertmanager: {url: http://am}\nchanels: {alerts: [a]}\n")); err == nil {
		t.Fatal("a misspelt key was accepted")
	}
}

func TestAlertmanagerIsRequired(t *testing.T) {
	if _, err := Parse(nil); err == nil || !strings.Contains(err.Error(), "alertmanager.url is required") {
		t.Fatal(err)
	}
}

func TestDurationsTakeDays(t *testing.T) {
	c, err := Parse([]byte("alertmanager: {url: http://am}\nincidents: {historyWindow: 3d, reopenWindow: 90m}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(c.Incidents.HistoryWindow) != 72*time.Hour || time.Duration(c.Incidents.ReopenWindow) != 90*time.Minute {
		t.Fatal(c.Incidents)
	}
}

func TestIntegrationsAreChecked(t *testing.T) {
	_, err := Parse([]byte(`
alertmanager: {url: http://am}
investigate: {ploeg: {url: http://ploeg}}
events: {webhooks: [{types: [opened, exploded]}]}
`))
	if err == nil {
		t.Fatal("accepted incomplete integrations")
	}
	for _, want := range []string{"investigate.ploeg needs team, target and tokenFile", "events.webhooks[0].url is required", `"exploded" is not an event type`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not say %q: %v", want, err)
		}
	}
}
