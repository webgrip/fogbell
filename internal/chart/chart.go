// Package chart draws an alert's series as a PNG for the incident thread: the value as a line, the
// threshold dashed and the moments the rule fired shaded. It needs no renderer, browser or font: the
// axis labels are drawn from a 5x7 bitmap font.
package chart

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"

	"github.com/fogbell-dev/fogbell/internal/format"
	"github.com/fogbell-dev/fogbell/internal/spark"
)

// Points is how many points a chart draws.
const Points = 240

// Width and Height are the chart's size in pixels.
const (
	Width  = 960
	Height = 320
	left   = 96
	right  = 24
	top    = 20
	bottom = 44
)

var (
	bg     = color.RGBA{255, 255, 255, 255}
	grid   = color.RGBA{230, 232, 235, 255}
	ink    = color.RGBA{87, 96, 106, 255}
	line   = color.RGBA{9, 105, 218, 255}
	limitC = color.RGBA{209, 36, 47, 255}
	firing = color.RGBA{252, 228, 230, 255}
)

var glyphs = map[rune][7]string{
	'0': {"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	'1': {"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	'2': {"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	'3': {"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "11110", "00001", "00001", "10001", "01110"},
	'6': {"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00010", "01100"},
	'.': {"00000", "00000", "00000", "00000", "00000", "01100", "01100"},
	'-': {"00000", "00000", "00000", "11111", "00000", "00000", "00000"},
	'+': {"00000", "00100", "00100", "11111", "00100", "00100", "00000"},
	'e': {"00000", "00000", "01110", "10001", "11111", "10000", "01110"},
	'k': {"10000", "10000", "10010", "10100", "11000", "10100", "10010"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'G': {"01110", "10001", "10000", "10111", "10001", "10001", "01111"},
	'd': {"00001", "00001", "01101", "10011", "10001", "10011", "01101"},
	'h': {"10000", "10000", "10110", "11001", "10001", "10001", "10001"},
	'n': {"00000", "00000", "10110", "11001", "10001", "10001", "10001"},
	'o': {"00000", "00000", "01110", "10001", "10001", "10001", "01110"},
	'w': {"00000", "00000", "10001", "10001", "10101", "10101", "01010"},
	'm': {"00000", "00000", "11010", "10101", "10101", "10001", "10001"},
}

type canvas struct {
	img *image.RGBA
}

func newCanvas(w, h int) *canvas {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = bg.R, bg.G, bg.B, 255
	}
	return &canvas{img: img}
}

func (c *canvas) rect(x0, y0, x1, y1 int, col color.RGBA) {
	b := c.img.Bounds()
	for y := max(0, y0); y < min(b.Dy(), y1); y++ {
		for x := max(0, x0); x < min(b.Dx(), x1); x++ {
			c.img.SetRGBA(x, y, col)
		}
	}
}

func (c *canvas) line(x0, y0, x1, y1 float64, col color.RGBA, width int) {
	steps := int(math.Max(math.Abs(x1-x0), math.Abs(y1-y0))) + 1
	for i := 0; i <= steps; i++ {
		x := x0 + (x1-x0)*float64(i)/float64(steps)
		y := y0 + (y1-y0)*float64(i)/float64(steps)
		px, py := int(x)-width/2, int(y)-width/2
		c.rect(px, py, px+width, py+width, col)
	}
}

func (c *canvas) text(x, y int, s string, col color.RGBA) {
	const scale = 2
	n := 0
	for _, ch := range s {
		if g, ok := glyphs[ch]; ok {
			for row, bits := range g {
				for colIdx, bit := range bits {
					if bit == '1' {
						c.rect(x+(n*6+colIdx)*scale, y+row*scale, x+(n*6+colIdx+1)*scale, y+(row+1)*scale, col)
					}
				}
			}
		}
		n++
	}
}

func textWidth(s string) int { return len([]rune(s)) * 6 * 2 }

// AxisText renders a window as "4 d", "6 h" or "90 m": the largest unit that divides it.
func AxisText(seconds int64) string {
	for _, u := range []struct {
		unit string
		size int64
	}{{"d", 86400}, {"h", 3600}} {
		if seconds >= u.size && seconds%u.size == 0 {
			return fmt.Sprintf("%d %s", seconds/u.size, u.unit)
		}
	}
	return fmt.Sprintf("%d m", seconds/60)
}

// PNG draws the values as a line over window seconds, the threshold dashed when given, and the points
// where fired is true shaded.
func PNG(values []spark.Value, fired []bool, limit *float64, window int64) ([]byte, error) {
	c := newCanvas(Width, Height)
	x0, x1 := float64(left), float64(Width-right)
	y0, y1 := float64(top), float64(Height-bottom)
	n := max(2, len(values))
	known := spark.Known(values)
	if limit != nil {
		known = append(known, *limit)
	}
	lo, hi := 0.0, 1.0
	if len(known) > 0 {
		lo, hi = known[0], known[0]
		for _, v := range known {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	whole := len(known) > 0 && hi-lo <= 8
	for _, v := range known {
		if v != math.Trunc(v) {
			whole = false
		}
	}
	var ticks []float64
	if whole {
		lo, hi = math.Min(lo, 0), math.Max(hi, lo+1)
		for t := int(lo); t <= int(hi); t++ {
			ticks = append(ticks, float64(t))
		}
	} else {
		if hi == lo {
			lo, hi = lo-1, hi+1
		}
		for k := 0; k < 5; k++ {
			ticks = append(ticks, lo+(hi-lo)*float64(k)/4)
		}
	}
	pad := (hi - lo) * 0.05
	lo, hi = lo-pad, hi+pad
	xAt := func(i int) float64 { return x0 + (x1-x0)*float64(i)/float64(n-1) }
	yAt := func(v float64) float64 { return y1 - (y1-y0)*(v-lo)/(hi-lo) }

	half := (x1 - x0) / float64(n-1) / 2
	for i, on := range fired {
		if on {
			c.rect(int(xAt(i)-half), int(y0), int(xAt(i)+half)+1, int(y1), firing)
		}
	}
	for _, v := range ticks {
		y := int(yAt(v))
		c.rect(int(x0), y, int(x1), y+1, grid)
		label := format.Number(v)
		c.text(int(x0)-10-textWidth(label), y-7, label, ink)
	}
	if limit != nil {
		y := int(yAt(*limit))
		for x := int(x0); x < int(x1); x += 16 {
			c.rect(x, y-1, min(int(x1), x+9), y+1, limitC)
		}
		label := format.Number(*limit)
		ly := y + 6
		if y-20 > int(y0) {
			ly = y - 20
		}
		c.text(int(x1)-textWidth(label)-4, ly, label, limitC)
	}
	var prev *[2]float64
	for i, v := range values {
		if !v.Known {
			prev = nil
			continue
		}
		p := [2]float64{xAt(i), yAt(v.V)}
		if prev != nil {
			c.line(prev[0], prev[1], p[0], p[1], line, 3)
		}
		prev = &p
	}
	for _, l := range []struct {
		i     int
		label string
	}{{0, "-" + AxisText(window)}, {n / 2, "-" + AxisText(window/2)}, {n - 1, "now"}} {
		x := int(xAt(l.i)) - textWidth(l.label)/2
		c.text(min(max(0, x), Width-textWidth(l.label)), int(y1)+14, l.label, ink)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, c.img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
