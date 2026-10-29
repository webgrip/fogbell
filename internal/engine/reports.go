package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/webgrip/fogbell/internal/alert"
	"github.com/webgrip/fogbell/internal/format"
	"github.com/webgrip/fogbell/internal/render"
	"github.com/webgrip/fogbell/internal/slack"
	"github.com/webgrip/fogbell/internal/state"
)

func (e *Engine) report(ctx context.Context, channel, text, event string, payload any) error {
	raw, _ := json.Marshal(payload)
	_, _, err := e.Slack.PostMessage(ctx, slack.Post{Channel: channel, Text: format.Cut(text, 3900),
		Metadata: &slack.Metadata{EventType: event, EventPayload: raw}})
	return err
}

// Digest posts the summary of the last 24 hours in a channel, once per day.
func (e *Engine) Digest(ctx context.Context, channelName, day string) (string, error) {
	channel := e.Slack.ChannelID(ctx, channelName)
	now := e.now()
	msgs := e.history(ctx, channel, now-86400)
	type item struct {
		msg *slack.Message
		st  *state.Incident
	}
	var incidents []item
	for i := range msgs {
		var d state.Digest
		if msgs[i].Payload(e.Config.Slack.Events.Digest, &d) && d.Day == day {
			return "already", nil
		}
		if st, ok := e.incident(&msgs[i]); ok {
			incidents = append(incidents, item{&msgs[i], st})
		}
	}
	silenced := e.silencedLines(ctx, channel, channelName)
	if len(incidents) == 0 && len(silenced) == 0 {
		return "quiet", nil
	}
	var open, closed, flappy []item
	for _, it := range incidents {
		if it.st.Resolved() {
			closed = append(closed, it)
		} else {
			open = append(open, it)
		}
		if it.st.Firings() > 2 {
			flappy = append(flappy, it)
		}
	}
	link := func(it item) string {
		name := it.st.Alertname
		if len(it.msg.Attachments) > 0 && it.msg.Attachments[0].Fallback != "" {
			name = it.msg.Attachments[0].Fallback
		}
		return "<" + format.Permalink(channel, it.msg.TS) + "|" + format.Escape(name) + ">"
	}
	lines := []string{fmt.Sprintf("*☀️ Last 24 hours: %d incident(s), %d still open*", len(incidents), len(open))}
	if len(silenced) > 0 {
		lines = append(lines, fmt.Sprintf("*Silenced now* (%d)", len(silenced)))
		lines = append(lines, silenced[:min(15, len(silenced))]...)
	}
	if len(open) > 0 {
		lines = append(lines, "*Open*")
		for _, it := range open[:min(15, len(open))] {
			who := " · nobody on it yet"
			if it.st.AckedBy != "" {
				who = " · 👀 <@" + it.st.AckedBy + ">"
			}
			quiet := ""
			if it.st.SilenceID != "" {
				quiet = " · 🔕 silenced"
			}
			lines = append(lines, "• "+link(it)+who+quiet)
		}
	}
	if len(closed) > 0 {
		lines = append(lines, fmt.Sprintf("*Resolved* (%d)", len(closed)))
		for _, it := range closed[:min(10, len(closed))] {
			lines = append(lines, "• "+link(it)+" · "+format.Duration(startOr(it.st.ResolvedAt, now)-startOr(it.st.StartedAt, now)))
		}
	}
	if len(flappy) > 0 {
		lines = append(lines, "*Flapping* (fired 3+ times in an hour: tune or fix the rule)")
		for _, it := range flappy[:min(5, len(flappy))] {
			lines = append(lines, fmt.Sprintf("• %s · %d×", link(it), it.st.Firings()))
		}
	}
	if err := e.report(ctx, channel, strings.Join(lines, "\n"), e.Config.Slack.Events.Digest, state.Digest{Day: day}); err != nil {
		return "", err
	}
	return "posted", nil
}

var (
	slackLink = regexp.MustCompile(`https://slack\.com/archives/\S+`)
	fromSlack = regexp.MustCompile(`(?is)\s*\(?from Slack by .*$`)
)

// silencedLines is the digest's "Silenced now": the active silences made from this channel's messages,
// and in the overview channel every active silence made elsewhere. Who, until, why.
func (e *Engine) silencedLines(ctx context.Context, channel, channelName string) []string {
	all, err := e.AM.Silences(ctx)
	if err != nil {
		e.Metrics.Degraded.WithLabelValues("silence_overview").Inc()
		e.Log.Warn("silences unavailable; digest without them", "error", err)
		return nil
	}
	var active []int
	for i, s := range all {
		if s.Status.State == "active" {
			active = append(active, i)
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return all[active[i]].EndsAt < all[active[j]].EndsAt })
	overview := strings.TrimPrefix(channelName, "#") == strings.TrimPrefix(e.Config.Channels.SilenceOverview, "#") &&
		e.Config.Channels.SilenceOverview != ""
	var lines []string
	for _, i := range active {
		s := all[i]
		var why, who, where string
		switch {
		case strings.HasPrefix(s.CreatedBy, "slack:"):
			if !strings.Contains(s.Comment, "/archives/"+channel+"/") {
				continue
			}
			why = strings.TrimSpace(fromSlack.ReplaceAllString(s.Comment, ""))
			who = strings.TrimPrefix(s.CreatedBy, "slack:")
			if m := slackLink.FindString(s.Comment); m != "" {
				where = " · <" + strings.TrimRight(m, ")") + "|message>"
			}
		case overview:
			why, who = strings.TrimSpace(s.Comment), s.CreatedBy
			if who == "" {
				who = "unknown"
			}
		default:
			continue
		}
		matchers := map[string]string{}
		for _, m := range s.Matchers {
			matchers[m.Name] = m.Value
		}
		reason := "no reason given"
		if why != "" {
			reason = "_" + format.Escape(format.Cut(why, 150)) + "_"
		}
		lines = append(lines, "• "+render.MatcherText(matchers)+" · "+format.Escape(who)+" · until "+
			format.Date(alert.ParseTime(s.EndsAt), format.Long, format.LongFallback)+" · "+reason+where)
	}
	return lines
}

// Rule is one rule's line in the noise report.
type Rule struct {
	Alertname string
	Incidents int
	Noise     int
	Flaps     int
	Short     int
	Score     int
}

// NoiseRanking ranks rules by how much attention they cost without being worth it: noise votes count
// three, every re-fire within the reopen window one, and every firing gone by itself within
// shortLived seconds one.
func NoiseRanking(incidents []*state.Incident, shortLived float64) []Rule {
	rules := map[string]*Rule{}
	for _, st := range incidents {
		name := st.Alertname
		if name == "" {
			name = "?"
		}
		r, ok := rules[name]
		if !ok {
			r = &Rule{Alertname: name}
			rules[name] = r
		}
		r.Incidents++
		r.Noise += int(st.NoiseVotes)
		r.Flaps += max(0, st.Firings()-1)
		if st.Resolved() && st.ResolvedAt-st.StartedAt > 0 && st.ResolvedAt-st.StartedAt < shortLived {
			r.Short++
		}
	}
	var out []Rule
	for _, r := range rules {
		r.Score = 3*r.Noise + r.Flaps + r.Short
		if r.Score > 0 {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Alertname < out[j].Alertname
	})
	return out
}

// NoiseReport posts the ranking of the rules that cost attention over the last seven days, once a week.
func (e *Engine) NoiseReport(ctx context.Context, channelName, week string) (string, error) {
	channel := e.Slack.ChannelID(ctx, channelName)
	now := e.now()
	for _, m := range e.history(ctx, channel, now-8*86400) {
		var r state.NoiseReport
		if m.Payload(e.Config.Slack.Events.NoiseReport, &r) && r.Week == week {
			return "already", nil
		}
	}
	type found struct {
		channel, ts string
		st          *state.Incident
	}
	var all []found
	channels := e.Config.AlertChannels()
	for _, name := range channels {
		ch := e.Slack.ChannelID(ctx, name)
		msgs := e.history(ctx, ch, now-7*86400)
		for i := range msgs {
			if st, ok := e.incident(&msgs[i]); ok {
				all = append(all, found{ch, msgs[i].TS, st})
			}
		}
	}
	sts := make([]*state.Incident, len(all))
	latest := map[string]found{}
	for i, f := range all {
		sts[i] = f.st
		name := f.st.Alertname
		if name == "" {
			name = "?"
		}
		if _, ok := latest[name]; !ok {
			latest[name] = f
		}
	}
	short := e.Config.Incidents.ShortLived.Seconds()
	ranking := NoiseRanking(sts, short)
	var lines []string
	if len(ranking) > 0 {
		lines = []string{
			fmt.Sprintf("*🙈 Noisy rules, last 7 days* (%d incidents in %d channels). Tune, fix or delete the top ones.", len(all), len(channels)),
			fmt.Sprintf("_Score: 3 per noise vote, 1 per re-fire within the hour, 1 per firing gone within %d min._", int(short/60)),
		}
		for _, r := range ranking[:min(10, len(ranking))] {
			f := latest[r.Alertname]
			var why []string
			if r.Noise > 0 {
				why = append(why, fmt.Sprintf("%d noise vote%s", r.Noise, format.Plural(r.Noise)))
			}
			if r.Flaps > 0 {
				why = append(why, fmt.Sprintf("%d re-fire%s", r.Flaps, format.Plural(r.Flaps)))
			}
			if r.Short > 0 {
				why = append(why, fmt.Sprintf("%d short-lived", r.Short))
			}
			lines = append(lines, fmt.Sprintf("• <%s|%s> · %d incident%s · %s", format.Permalink(f.channel, f.ts),
				format.Escape(r.Alertname), r.Incidents, format.Plural(r.Incidents), strings.Join(why, ", ")))
		}
	} else {
		lines = []string{fmt.Sprintf("*🙈 Noisy rules, last 7 days*: none. %d incidents, no noise votes, re-fires or short-lived firings.", len(all))}
	}
	if err := e.report(ctx, channel, strings.Join(lines, "\n"), e.Config.Slack.Events.NoiseReport, state.NoiseReport{Week: week}); err != nil {
		return "", err
	}
	return "posted", nil
}

// Tick posts what is due at now: every day at the digest time the digest, once per channel; on the
// noise report's weekday also the noise report. Message metadata dedupes restarts, and the window is
// ten minutes so a two-minute ticker cannot miss it.
func (e *Engine) Tick(ctx context.Context, now time.Time) []string {
	local := now.In(e.Config.Location())
	h, m, _ := e.Config.DigestClock()
	due := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, e.Config.Location())
	if local.Before(due) || local.Sub(due) >= 10*time.Minute {
		return nil
	}
	var done []string
	day := local.Format("2006-01-02")
	for _, ch := range e.Config.DigestChannels() {
		res, err := e.Digest(ctx, ch, day)
		if err != nil {
			e.Log.Warn("digest failed", "channel", ch, "error", err)
			res = "error"
		}
		done = append(done, "digest "+ch+": "+res)
	}
	wd, _ := e.Config.NoiseWeekday()
	if local.Weekday() == wd && e.Config.Channels.NoiseReport != "" {
		y, w := local.ISOWeek()
		res, err := e.NoiseReport(ctx, e.Config.Channels.NoiseReport, fmt.Sprintf("%d-W%02d", y, w))
		if err != nil {
			e.Log.Warn("noise report failed", "error", err)
			res = "error"
		}
		done = append(done, "noise report: "+res)
	}
	return done
}
