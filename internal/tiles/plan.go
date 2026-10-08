// Package tiles plans, aligns and blends the overlapping crops of a tiled
// upscale: an image is cut into crops whose aspect ratios the editing model
// supports, each crop is re-rendered at high resolution by the model, and the
// returned tiles are registered against the image and blended into a larger
// canvas. It is pure image processing: no network, no model calls.
package tiles

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Box is a pixel rectangle; X and Y are its top-left corner.
type Box struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"width"`
	H int `json:"height"`
}

// X1 is the exclusive right edge.
func (b Box) X1() int { return b.X + b.W }

// Y1 is the exclusive bottom edge.
func (b Box) Y1() int { return b.Y + b.H }

func (b Box) ratio() float64 { return float64(b.W) / float64(b.H) }

func (b Box) contains(o Box) bool {
	return o.X >= b.X && o.Y >= b.Y && o.X1() <= b.X1() && o.Y1() <= b.Y1()
}

// Ratio is an output aspect ratio the editing model supports, e.g. "4:3".
type Ratio struct {
	Name  string
	Value float64 // width / height
}

// ParseRatios parses "W:H" names, skipping malformed ones.
func ParseRatios(names []string) []Ratio {
	var out []Ratio
	for _, n := range names {
		w, h, ok := strings.Cut(n, ":")
		if !ok {
			continue
		}
		fw, err1 := strconv.ParseFloat(strings.TrimSpace(w), 64)
		fh, err2 := strconv.ParseFloat(strings.TrimSpace(h), 64)
		if err1 != nil || err2 != nil || fw <= 0 || fh <= 0 {
			continue
		}
		out = append(out, Ratio{Name: n, Value: fw / fh})
	}
	return out
}

// Tile is one planned crop.
type Tile struct {
	Index int    `json:"tile"` // 1-based
	Label string `json:"label"`
	// Box is the crop in plan-image pixels.
	Box Box `json:"box"`
	// Core is the area the tile is responsible for: its grid cell or the
	// requested region, before padding.
	Core Box `json:"core"`
	// AspectRatio is the supported ratio the crop was snapped to; request it
	// from the editing model so the tile comes back as a scaled crop.
	AspectRatio string `json:"aspectRatio"`
	// Outside is set when the crop extends past the image (no supported
	// ratio fits inside it): the outside is filled by mirroring the image
	// and discarded when stitching.
	Outside bool `json:"outside,omitempty"`
}

// Region is a requested area in fractions (0-1) of the image.
type Region struct {
	X, Y, W, H float64
	Label      string
}

// MinTilePixels is the smallest crop side worth sending to the model.
const MinTilePixels = 16

// Grid plans an n x n grid over a w x h image. Each cell is padded by padding
// (a fraction of the cell size) on every side that is not on the image
// border, then grown to the nearest supported aspect ratio. A 1 x 1 grid is
// the whole image as one tile.
func Grid(w, h, n int, padding float64, ratios []Ratio) ([]Tile, error) {
	if n < 1 {
		return nil, fmt.Errorf("grid must be at least 1")
	}
	if w/n < MinTilePixels || h/n < MinTilePixels {
		return nil, fmt.Errorf("a %dx%d image is too small for a %dx%d grid", w, h, n, n)
	}
	xs, ys := cuts(w, n), cuts(h, n)
	var out []Tile
	for r := range n {
		for c := range n {
			core := Box{X: xs[c], Y: ys[r], W: xs[c+1] - xs[c], H: ys[r+1] - ys[r]}
			t := plan(core, pad(core, padding, w, h), w, h, ratios)
			t.Index = len(out) + 1
			t.Label = fmt.Sprintf("r%dc%d", r+1, c+1)
			out = append(out, t)
		}
	}
	return out, nil
}

// Regions plans one crop per requested region (fractions of a w x h image),
// padded by padding (a fraction of the region size) and grown to the nearest
// supported aspect ratio.
func Regions(w, h int, regions []Region, padding float64, ratios []Ratio) ([]Tile, error) {
	var out []Tile
	for i, r := range regions {
		x0, y0 := clampf(r.X, 0, 1), clampf(r.Y, 0, 1)
		x1, y1 := clampf(r.X+r.W, 0, 1), clampf(r.Y+r.H, 0, 1)
		// The epsilon keeps 0.4+0.2 from rounding up to an extra pixel.
		core := Box{X: int(math.Floor(x0*float64(w) + 1e-6)), Y: int(math.Floor(y0*float64(h) + 1e-6))}
		core.W = int(math.Ceil(x1*float64(w)-1e-6)) - core.X
		core.H = int(math.Ceil(y1*float64(h)-1e-6)) - core.Y
		if r.W <= 0 || r.H <= 0 || core.W < MinTilePixels || core.H < MinTilePixels {
			return nil, fmt.Errorf("region %d (%s) is empty or smaller than %d pixels on a %dx%d image", i+1, r.Label, MinTilePixels, w, h)
		}
		t := plan(core, pad(core, padding, w, h), w, h, ratios)
		t.Index = i + 1
		t.Label = r.Label
		out = append(out, t)
	}
	return out, nil
}

// cuts splits n into near-equal integer intervals.
func cuts(size, n int) []int {
	out := make([]int, n+1)
	for i := range out {
		out[i] = int(math.Round(float64(i) * float64(size) / float64(n)))
	}
	return out
}

func pad(core Box, padding float64, w, h int) Box {
	px := int(math.Round(padding * float64(core.W)))
	py := int(math.Round(padding * float64(core.H)))
	x0, y0 := max(core.X-px, 0), max(core.Y-py, 0)
	x1, y1 := min(core.X1()+px, w), min(core.Y1()+py, h)
	return Box{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

func plan(core, padded Box, w, h int, ratios []Ratio) Tile {
	box, name := snap(padded, core, w, h, ratios)
	img := Box{W: w, H: h}
	return Tile{Box: box, Core: core, AspectRatio: name, Outside: !img.contains(box)}
}

// snap returns a box containing core with one of the supported ratios. It
// prefers growing the padded box inside the image (least added area), then
// shrinking it while keeping core covered, and as a last resort grows it past
// the image border (the caller mirrors the image there). With no ratios it
// returns the padded box unchanged.
func snap(p, core Box, w, h int, ratios []Ratio) (Box, string) {
	if len(ratios) == 0 {
		return p, ""
	}
	best, name, bestArea := Box{}, "", math.MaxInt
	for _, r := range ratios {
		bw, bh := p.W, p.H
		if p.ratio() < r.Value {
			bw = int(math.Round(float64(bh) * r.Value))
		} else {
			bh = int(math.Round(float64(bw) / r.Value))
		}
		bw, bh = max(bw, p.W), max(bh, p.H)
		if bw > w || bh > h {
			continue
		}
		if a := bw * bh; a < bestArea {
			best, name, bestArea = place(p, bw, bh, Box{W: w, H: h}), r.Name, a
		}
	}
	if name != "" {
		return best, name
	}
	bestArea = 0
	for _, r := range ratios {
		bw, bh := p.W, p.H
		if p.ratio() > r.Value {
			bw = int(math.Round(float64(bh) * r.Value))
		} else {
			bh = int(math.Round(float64(bw) / r.Value))
		}
		bw, bh = min(bw, p.W), min(bh, p.H)
		if bw < core.W || bh < core.H {
			continue
		}
		if a := bw * bh; a > bestArea {
			best, name, bestArea = place(core, bw, bh, p), r.Name, a
		}
	}
	if name != "" {
		return best, name
	}
	// Grow past the border, centered on the padded box.
	bestArea = math.MaxInt
	for _, r := range ratios {
		bw, bh := p.W, p.H
		if p.ratio() < r.Value {
			bw = int(math.Round(float64(bh) * r.Value))
		} else {
			bh = int(math.Round(float64(bw) / r.Value))
		}
		bw, bh = max(bw, p.W), max(bh, p.H)
		if a := bw * bh; a < bestArea {
			best, name, bestArea = Box{X: p.X + (p.W-bw)/2, Y: p.Y + (p.H-bh)/2, W: bw, H: bh}, r.Name, a
		}
	}
	return best, name
}

// place centers a bw x bh box on around and moves it inside bounds.
func place(around Box, bw, bh int, bounds Box) Box {
	x := around.X + (around.W-bw)/2
	y := around.Y + (around.H-bh)/2
	x = min(max(x, bounds.X), bounds.X1()-bw)
	y = min(max(y, bounds.Y), bounds.Y1()-bh)
	return Box{X: x, Y: y, W: bw, H: bh}
}

func clampf(v, lo, hi float64) float64 { return math.Min(math.Max(v, lo), hi) }
