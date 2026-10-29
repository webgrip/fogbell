package chart

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/webgrip/fogbell/internal/spark"
)

func TestAxisText(t *testing.T) {
	for s, want := range map[int64]string{4 * 86400: "4 d", 6 * 3600: "6 h", 5400: "90 m"} {
		if got := AxisText(s); got != want {
			t.Errorf("AxisText(%d) = %q", s, got)
		}
	}
}

func TestPNGDecodesAtItsSize(t *testing.T) {
	vs := spark.Of(0, 1, 2, 0, 3)
	vs[0].Known, vs[3].Known = false, false
	limit := 2.5
	raw, err := PNG(vs, []bool{false, true, true, false, false}, &limit, 3600)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != Width || b.Dy() != Height {
		t.Fatal(b)
	}
	white := func(x, y int) bool {
		r, g, b, _ := img.At(x, y).RGBA()
		return r>>8 == 255 && g>>8 == 255 && b>>8 == 255
	}
	second := left + (Width-right-left)/4
	if white(second, top+5) {
		t.Fatal("the firing shade is missing at the second point")
	}
	if !white(left+(Width-right-left)*3/4, top+5) {
		t.Fatal("the fourth point is shaded but did not fire")
	}
}
