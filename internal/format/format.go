// Package format holds the text helpers every Slack message fogbell writes shares.
package format

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Long is the Slack date format for a day and a time, with its UTC fallback.
const (
	Long         = "{date_short_pretty} {time}"
	LongFallback = "Jan 02 15:04 UTC"
	Time         = "{time}"
	TimeFallback = "15:04 UTC"
)

// Escape escapes the three characters Slack mrkdwn treats as control characters.
func Escape(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	return strings.ReplaceAll(text, ">", "&gt;")
}

// Date renders a timestamp that Slack shows in each reader's own time zone, with a UTC fallback.
func Date(ts float64, slackFormat, fallbackLayout string) string {
	fallback := time.Unix(int64(ts), 0).UTC().Format(fallbackLayout)
	return fmt.Sprintf("<!date^%d^%s|%s>", int64(ts), slackFormat, fallback)
}

// Duration renders seconds as "25 min", "1 h 30 min" or "2 d 3 h".
func Duration(seconds float64) string {
	s := int64(seconds)
	switch {
	case s < 3600:
		return fmt.Sprintf("%d min", max(1, s/60))
	case s < 86400:
		h, m := s/3600, (s/60)%60
		if m != 0 {
			return fmt.Sprintf("%d h %d min", h, m)
		}
		return fmt.Sprintf("%d h", h)
	default:
		d, h := s/86400, (s/3600)%24
		if h != 0 {
			return fmt.Sprintf("%d d %d h", d, h)
		}
		return fmt.Sprintf("%d d", d)
	}
}

// Ordinal renders 1 as "1st", 2 as "2nd", 11 as "11th".
func Ordinal(n int) string {
	suffix := "th"
	if n%100 < 10 || n%100 > 20 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// Plural returns "s" unless n is one.
func Plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Number renders a value with three significant digits and a k, M or G suffix.
func Number(v float64) string {
	for _, u := range []struct {
		div  float64
		unit string
	}{{1e9, "G"}, {1e6, "M"}, {1e3, "k"}} {
		if math.Abs(v) >= u.div {
			return sig3(v/u.div) + u.unit
		}
	}
	return sig3(v)
}

func sig3(v float64) string {
	return strconv.FormatFloat(v, 'g', 3, 64)
}

var (
	slackDate  = regexp.MustCompile(`<!date\^\d+\^[^|>]*\|([^>]*)>`)
	mention    = regexp.MustCompile(`<@(\w+)>`)
	broadcast  = regexp.MustCompile(`<!(channel|here)>`)
	namedLink  = regexp.MustCompile(`<(https?://[^|>]+)\|([^>]*)>`)
	bareLink   = regexp.MustCompile(`<(https?://[^>]+)>`)
	codeFences = strings.NewReplacer("```", "ˋˋˋ")
)

// Plain turns Slack mrkdwn into text for a code block: links become their label, mentions and dates
// stay readable, and no code fence survives.
func Plain(text string) string {
	text = slackDate.ReplaceAllString(text, "$1")
	text = mention.ReplaceAllString(text, "@$1")
	text = broadcast.ReplaceAllString(text, "@$1")
	text = namedLink.ReplaceAllString(text, "$2 ($1)")
	text = bareLink.ReplaceAllString(text, "$1")
	text = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(text)
	return strings.TrimSpace(codeFences.Replace(text))
}

// Cut shortens text to at most n runes.
func Cut(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	return string(r[:n])
}

// Permalink is the archive link of a Slack message.
func Permalink(channel, ts string) string {
	return "https://slack.com/archives/" + channel + "/p" + strings.ReplaceAll(ts, ".", "")
}
