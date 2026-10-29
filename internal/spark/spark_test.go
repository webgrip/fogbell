package spark

import "testing"

func TestLineScalesToTheThreshold(t *testing.T) {
	limit := 100.0
	if got := Line(Of(10, 20, 30, 40, 50, 60), &limit); got != "▁▁▂▃▄▅" {
		t.Fatal(got)
	}
	vs := Of(1, 2, 0, 8)
	vs[2].Known = false
	three := 3.0
	if got := Line(vs, &three); got != "▁▂ █" {
		t.Fatal(got)
	}
	if got := Line(Of(5, 5, 5), nil); got != "▄▄▄" {
		t.Fatal(got)
	}
}

func TestTrendAgainstAndRuns(t *testing.T) {
	if got := Trend([]float64{10, 20, 30, 40, 50, 60}); got != "↗ rising" {
		t.Fatal(got)
	}
	if got := Trend([]float64{5, 5, 5, 5}); got != "→ steady" {
		t.Fatal(got)
	}
	if got := Against(">", 3, 8); got != "threshold > 3 (2.7×)" {
		t.Fatal(got)
	}
	if got := Against("<", 10, 2); got != "threshold < 10 (20% of it)" {
		t.Fatal(got)
	}
	vs := Of(0, 1, 1, 0, 1)
	if Runs(vs) != 2 || Timeline(vs) != "▁██▁█" {
		t.Fatal(Runs(vs), Timeline(vs))
	}
}

func TestOnGridPlacesSamplesFromNow(t *testing.T) {
	got := OnGrid([]Sample{{T: 940, V: 1}, {T: 1000, V: 2}}, 1000, 60, 3)
	if got[0].Known || got[1].V != 1 || got[2].V != 2 {
		t.Fatal(got)
	}
}
