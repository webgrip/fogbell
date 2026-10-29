package promql

import "testing"

func TestPlainSeries(t *testing.T) {
	for expr, want := range map[string]string{
		"x > 15":                             "x",
		"x > bool 3":                         "x",
		`x{l=~"a|b"} >= 0.9`:                 `x{l=~"a|b"}`,
		"rate(x[5m])":                        "rate(x[5m])",
		"max by (a) (x < bool y)[1h:1m] > 2": "(max(x <bool y) by(a))[1h:1m]",
		"# comment\nup == 0":                 "up",
		"a > b":                              "",
		"absent(up == 1)":                    "",
		"sum(a) > 0 unless sum(b) > 0":       "",
		"a and b":                            "",
		"up or 1":                            "",
		"x > 1 or y > 1":                     "",
		"x > 1 < 5":                          "",
		`x{msg="a > b"} > 1`:                 `x{msg="a > b"}`,
		"delta(x[1h]) + 1":                   "delta(x[1h]) + 1",
		"not a ( valid expression":           "",
		"":                                   "",
		"max_over_time(up[5m]) < 1 and on() vector(1)": "",
	} {
		if got := PlainSeries(expr); got != want {
			t.Errorf("PlainSeries(%q) = %q, want %q", expr, got, want)
		}
	}
}

func TestUnwrapAndWindow(t *testing.T) {
	for expr, want := range map[string][2]string{
		"delta(x[1h])":            {"x", "delta"},
		`changes(up{a="b"}[10m])`: {`up{a="b"}`, "changes"},
		"delta(x[1h]) + 1":        {"delta(x[1h]) + 1", ""},
		"rate(x[5m])":             {"rate(x[5m])", ""},
		"delta((sum(q))[1d:30m])": {"sum(q)", "delta"},
	} {
		if s, fn := Unwrap(expr); s != want[0] || fn != want[1] {
			t.Errorf("Unwrap(%q) = %q, %q", expr, s, fn)
		}
	}
	for expr, want := range map[string]int64{
		"rate(x[5m]) > 1":        3 * 3600,
		"delta((x)[1d:30m]) > 0": 4 * 86400,
		"delta(x[1w])":           7 * 86400,
		"x":                      3 * 3600,
	} {
		if got := Window(expr, 3*3600); got != want {
			t.Errorf("Window(%q) = %d, want %d", expr, got, want)
		}
	}
	if op, n, ok := Threshold("x > bool 3"); !ok || op != ">" || n != 3 {
		t.Fatal(op, n, ok)
	}
	if _, _, ok := Threshold("a > b"); ok {
		t.Fatal("a vector is not a threshold")
	}
}
