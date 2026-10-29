package format

import "testing"

func TestDuration(t *testing.T) {
	for s, want := range map[float64]string{30: "1 min", 1500: "25 min", 3600: "1 h", 5400: "1 h 30 min", 86400: "1 d", 90000: "1 d 1 h"} {
		if got := Duration(s); got != want {
			t.Errorf("Duration(%v) = %q, want %q", s, got, want)
		}
	}
}

func TestOrdinalAndNumber(t *testing.T) {
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 112: "112th"} {
		if got := Ordinal(n); got != want {
			t.Errorf("Ordinal(%d) = %q", n, got)
		}
	}
	for v, want := range map[float64]string{8: "8", 2.5: "2.5", 1234: "1.23k", 2500000: "2.5M", 3e9: "3G", 0.000012: "1.2e-05", -1500: "-1.5k"} {
		if got := Number(v); got != want {
			t.Errorf("Number(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestEscapePlainAndDate(t *testing.T) {
	if got := Escape("a <b> & c"); got != "a &lt;b&gt; &amp; c" {
		t.Fatal(got)
	}
	in := "<@U1> is on it at <!date^1793089000^{time}|08:16 UTC>, see <https://x.example|the doc> and ```code```"
	if got := Plain(in); got != "@U1 is on it at 08:16 UTC, see the doc (https://x.example) and ˋˋˋcodeˋˋˋ" {
		t.Fatal(got)
	}
	if got := Date(1793089000, Time, TimeFallback); got != "<!date^1793089000^{time}|08:16 UTC>" {
		t.Fatal(got)
	}
	if got := Permalink("C1", "1793089000.000100"); got != "https://slack.com/archives/C1/p1793089000000100" {
		t.Fatal(got)
	}
}
