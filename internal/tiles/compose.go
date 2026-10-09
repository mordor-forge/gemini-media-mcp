package tiles

import (
	"image"
	"math"
	"runtime"
	"sync"
)

// Placement is an aligned tile ready to paint.
type Placement struct {
	Box   Box
	Align Alignment
	// Feather is the blend width of each side (left, top, right, bottom) in
	// plan-image pixels; 0 means a hard edge.
	Feather [4]float64
	// Border marks sides on the image border: the tile is extended to the
	// canvas edge there instead of blending into the base.
	Border [4]bool
}

// Feathers sets Feather and Border for each placement. frac is the blend
// width as a fraction of the tile's shorter side; next to a neighboring tile
// it is limited to half the overlap, so that across every overlap at least
// one tile is fully opaque and the base never shows through a seam. Overlaps
// are measured where the tiles were aligned, not where they were cut.
func Feathers(ps []Placement, w, h int, frac float64) {
	rs := make([]extent, len(ps))
	for i := range ps {
		rs[i] = ps[i].extent()
	}
	for i := range ps {
		b, r := ps[i].Box, rs[i]
		f := frac * float64(min(b.W, b.H))
		// A tile cut against an edge of the image covers it out to that
		// edge, unless alignment moved it inward by more than half a pixel:
		// stretching it would smear its outer pixels, so the uncovered
		// strip keeps the base, faded in over about its width.
		atEdge := [4]bool{b.X <= 0, b.Y <= 0, b.X1() >= w, b.Y1() >= h}
		gap := [4]float64{r.x0, r.y0, float64(w) - r.x1, float64(h) - r.y1}
		for side := range 4 {
			ps[i].Border[side] = atEdge[side] && gap[side] <= 0.5
			if ps[i].Border[side] {
				ps[i].Feather[side] = 0
				continue
			}
			ps[i].Feather[side] = f
			if d, ok := neighborDepth(rs, i, side); ok {
				ps[i].Feather[side] = math.Min(f, d/2)
			} else if atEdge[side] {
				ps[i].Feather[side] = math.Min(f, math.Max(gap[side], 1))
			}
		}
	}
}

// extent is a rectangle in plan-image pixels.
type extent struct{ x0, y0, x1, y1 float64 }

// extent is where the placement's tile lands in plan-image pixels: its box,
// scaled about the center and shifted by the alignment.
func (p Placement) extent() extent {
	b, a := p.Box, p.Align
	sx, sy := a.ScaleX, a.ScaleY
	if sx == 0 || sy == 0 {
		sx, sy = 1, 1 // not aligned (tests): the box itself
	}
	cx := float64(b.X) + float64(b.W)/2 + a.ShiftX
	cy := float64(b.Y) + float64(b.H)/2 + a.ShiftY
	hw, hh := sx*float64(b.W)/2, sy*float64(b.H)/2
	return extent{cx - hw, cy - hh, cx + hw, cy + hh}
}

// neighborDepth is how far the closest-fitting neighbor overlaps tile i
// across side (left, top, right, bottom). Only neighbors that cover at least
// a quarter of that side count.
func neighborDepth(rs []extent, i, side int) (float64, bool) {
	b := rs[i]
	depth, found := math.Inf(1), false
	for j, o := range rs {
		if j == i {
			continue
		}
		var d, along, length float64
		switch side {
		case 0: // left: o extends past b's left edge into b
			if o.x0 >= b.x0 || o.x1 <= b.x0 {
				continue
			}
			d, along, length = math.Min(o.x1, b.x1)-b.x0, overlap(o.y0, o.y1, b.y0, b.y1), b.y1-b.y0
		case 1:
			if o.y0 >= b.y0 || o.y1 <= b.y0 {
				continue
			}
			d, along, length = math.Min(o.y1, b.y1)-b.y0, overlap(o.x0, o.x1, b.x0, b.x1), b.x1-b.x0
		case 2:
			if o.x1 <= b.x1 || o.x0 >= b.x1 {
				continue
			}
			d, along, length = b.x1-math.Max(o.x0, b.x0), overlap(o.y0, o.y1, b.y0, b.y1), b.y1-b.y0
		case 3:
			if o.y1 <= b.y1 || o.y0 >= b.y1 {
				continue
			}
			d, along, length = b.y1-math.Max(o.y0, b.y0), overlap(o.x0, o.x1, b.x0, b.x1), b.x1-b.x0
		}
		if 4*along < length {
			continue
		}
		depth, found = math.Min(depth, d), true
	}
	return depth, found
}

func overlap(a0, a1, b0, b1 float64) float64 { return math.Max(0, math.Min(a1, b1)-math.Max(a0, b0)) }

// weightScale is the fixed-point unit of the weight buffer.
const weightScale = 1024

// Canvas is the output image being assembled. Tiles are blended as a
// weighted average; the base keeps whatever weight the tiles leave
// (1 - their total, floored at 0), so it shows only where no tile reaches.
type Canvas struct {
	Img    *image.RGBA
	w, h   int
	sx, sy float64 // canvas pixels per plan pixel
	wt     []uint16
	bare   int // pixels no reserved placement reaches
}

// NewCanvas scales base (the plan image) to w x h. When base is already an
// *image.RGBA of that size it is used in place, so finish aligning against
// it before painting.
func NewCanvas(base image.Image, w, h int) *Canvas {
	pb := base.Bounds()
	c := &Canvas{w: w, h: h, sx: float64(w) / float64(pb.Dx()), sy: float64(h) / float64(pb.Dy())}
	if rgba, ok := base.(*image.RGBA); ok && pb == image.Rect(0, 0, w, h) && rgba.Stride == 4*w {
		c.Img = rgba
	} else {
		c.Img = Resize(base, pb, w, h)
	}
	return c
}

// footprint is a placement's extent on the canvas.
type footprint struct {
	x0, y0, x1, y1 float64 // where the tile maps (canvas px)
	px0, py0       int     // painted area (tile extended to borders), exclusive max:
	px1, py1       int
	fl, ft, fr, fb float64 // feathers in canvas px
}

func (c *Canvas) footprint(p Placement) footprint {
	r := p.extent()
	f := footprint{
		x0: r.x0 * c.sx, x1: r.x1 * c.sx, y0: r.y0 * c.sy, y1: r.y1 * c.sy,
		fl: p.Feather[0] * c.sx, ft: p.Feather[1] * c.sy, fr: p.Feather[2] * c.sx, fb: p.Feather[3] * c.sy,
	}
	f.px0, f.py0 = int(math.Floor(f.x0)), int(math.Floor(f.y0))
	f.px1, f.py1 = int(math.Ceil(f.x1)), int(math.Ceil(f.y1))
	if p.Border[0] {
		f.px0 = 0
	}
	if p.Border[1] {
		f.py0 = 0
	}
	if p.Border[2] {
		f.px1 = c.w
	}
	if p.Border[3] {
		f.py1 = c.h
	}
	f.px0, f.py0 = max(f.px0, 0), max(f.py0, 0)
	f.px1, f.py1 = min(f.px1, c.w), min(f.py1, c.h)
	return f
}

// weight is the blend weight (0-1) of the placement at canvas pixel (x, y).
func (f *footprint) weight(p *Placement, x, y int) float64 {
	cx, cy := float64(x)+0.5, float64(y)+0.5
	return side(p.Border[0], cx-f.x0, f.fl) * side(p.Border[1], cy-f.y0, f.ft) *
		side(p.Border[2], f.x1-cx, f.fr) * side(p.Border[3], f.y1-cy, f.fb)
}

func side(border bool, dist, feather float64) float64 {
	switch {
	case border:
		return 1
	case dist <= 0:
		return 0
	case feather <= 0 || dist >= feather:
		return 1
	}
	t := dist / feather
	return t * t * (3 - 2*t)
}

// Reserve records the weights of every placement that will be painted, so
// the base gets only the weight the tiles leave. Call it once, before Paint.
func (c *Canvas) Reserve(ps []Placement) {
	c.wt = make([]uint16, c.w*c.h)
	for i := range ps {
		p := &ps[i]
		f := c.footprint(*p)
		rows(f.py0, f.py1, func(y int) {
			for x := f.px0; x < f.px1; x++ {
				i := y*c.w + x
				c.wt[i] = uint16(min(uint32(c.wt[i])+uint32(math.Round(f.weight(p, x, y)*weightScale)), math.MaxUint16))
			}
		})
	}
	c.bare = 0
	for i, a := range c.wt {
		if a == 0 {
			c.bare++
		}
		c.wt[i] = uint16(max(weightScale-int(a), 0))
	}
}

// Prepared is a tile resampled to its place on the canvas. Preparing reads
// nothing from the canvas, so the next tile can be prepared while another
// is painted.
type Prepared struct {
	p      Placement
	f      footprint
	sw, sh int         // resampled size of the whole tile
	src    *image.RGBA // the part of it painted (Rect within sw x sh)
}

// Prepare resamples tile (the model's output for p) to its footprint.
// Only the part that lands on the canvas is computed: a tile reaching far
// past the canvas would otherwise allocate all of its resampled size.
func (c *Canvas) Prepare(p Placement, tile image.Image) *Prepared {
	f, sw, sh, win := c.window(p)
	return &Prepared{p: p, f: f, sw: sw, sh: sh, src: ResizeWindow(tile, tile.Bounds(), sw, sh, win)}
}

// PreparedPixels is the size in pixels of what Prepare allocates for p.
func (c *Canvas) PreparedPixels(p Placement) int {
	_, _, _, win := c.window(p)
	return win.Dx() * win.Dy()
}

// window is p's footprint, the size the whole tile resamples to, and the
// part of that painting reads.
func (c *Canvas) window(p Placement) (footprint, int, int, image.Rectangle) {
	f := c.footprint(p)
	sw := max(int(math.Round(f.x1-f.x0)), 1)
	sh := max(int(math.Round(f.y1-f.y0)), 1)
	kx, ky := float64(sw)/(f.x1-f.x0), float64(sh)/(f.y1-f.y0)
	x0, x1 := readSpan(f.px0, f.px1, f.x0, kx, sw)
	y0, y1 := readSpan(f.py0, f.py1, f.y0, ky, sh)
	return f, sw, sh, image.Rect(x0, y0, x1, y1)
}

// readSpan is the range of resampled pixels, within [0, n), that painting
// canvas pixels p0 to p1 (exclusive) samples: the mapped range plus the
// bilinear neighbors, and at least one pixel.
func readSpan(p0, p1 int, f0, k float64, n int) (int, int) {
	lo := int(math.Floor((float64(p0)+0.5-f0)*k-0.5)) - 1
	hi := int(math.Ceil((float64(p1)-0.5-f0)*k-0.5)) + 2
	lo = min(max(lo, 0), n-1)
	hi = min(max(hi, lo+1), n)
	return lo, hi
}

// Paint blends a prepared tile into the canvas. Call Reserve first with
// every placement that will be painted.
func (c *Canvas) Paint(pp *Prepared) {
	if c.wt == nil {
		c.Reserve([]Placement{pp.p})
	}
	p, f, src := pp.p, pp.f, pp.src
	kx, ky := float64(pp.sw)/(f.x1-f.x0), float64(pp.sh)/(f.y1-f.y0)
	// src holds only a window of the resampled tile.
	ox, oy := float64(src.Rect.Min.X), float64(src.Rect.Min.Y)
	b := p.Box
	rows(f.py0, f.py1, func(y int) {
		v := (float64(y)+0.5-f.y0)*ky - 0.5 - oy
		nv := ((float64(y)+0.5)/c.sy - float64(b.Y)) / float64(b.H)
		for x := f.px0; x < f.px1; x++ {
			q := uint32(math.Round(f.weight(&p, x, y) * weightScale))
			if q == 0 {
				continue
			}
			i := y*c.w + x
			wn := uint32(c.wt[i]) + q
			a := float32(q) / float32(wn)
			c.wt[i] = uint16(min(wn, math.MaxUint16))
			u := (float64(x)+0.5-f.x0)*kx - 0.5 - ox
			px := sampleRGBA(src, u, v)
			off := p.Align.Color.At(((float64(x)+0.5)/c.sx-float64(b.X))/float64(b.W), nv)
			d := c.Img.Pix[4*i : 4*i+4]
			for ch := range 3 {
				t := min(max(px[ch]+off[ch], 0), 255)
				d[ch] = uint8(math.Round(float64(float32(d[ch]) + (t-float32(d[ch]))*a)))
			}
			d[3] = uint8(math.Round(float64(float32(d[3]) + (px[3]-float32(d[3]))*a)))
		}
	})
}

// Uncovered is the fraction of the canvas that no placement passed to
// Reserve reaches, which keeps the base image (1 before Reserve).
func (c *Canvas) Uncovered() float64 {
	if c.wt == nil {
		return 1
	}
	return float64(c.bare) / float64(c.w*c.h)
}

// Finish releases the weight buffer and returns the assembled image.
func (c *Canvas) Finish() *image.RGBA {
	c.wt = nil
	return c.Img
}

// sampleRGBA bilinearly samples img at pixel coordinates (u, v), clamping
// to the edges.
func sampleRGBA(img *image.RGBA, u, v float64) [4]float32 {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	u = math.Min(math.Max(u, 0), float64(w-1))
	v = math.Min(math.Max(v, 0), float64(h-1))
	x0, y0 := int(u), int(v)
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)
	ax, ay := float32(u-float64(x0)), float32(v-float64(y0))
	p00 := img.Pix[y0*img.Stride+4*x0:]
	p10 := img.Pix[y0*img.Stride+4*x1:]
	p01 := img.Pix[y1*img.Stride+4*x0:]
	p11 := img.Pix[y1*img.Stride+4*x1:]
	var out [4]float32
	for ch := range 4 {
		top := float32(p00[ch])*(1-ax) + float32(p10[ch])*ax
		bot := float32(p01[ch])*(1-ax) + float32(p11[ch])*ax
		out[ch] = top*(1-ay) + bot*ay
	}
	return out
}

// rows runs fn for each y in [y0, y1) across CPUs.
func rows(y0, y1 int, fn func(y int)) {
	n := y1 - y0
	if n <= 0 {
		return
	}
	workers := min(runtime.GOMAXPROCS(0), n)
	var wg sync.WaitGroup
	chunk := (n + workers - 1) / workers
	for start := y0; start < y1; start += chunk {
		end := min(start+chunk, y1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for y := start; y < end; y++ {
				fn(y)
			}
		}()
	}
	wg.Wait()
}
