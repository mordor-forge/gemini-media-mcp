package tiles

import (
	"fmt"
	"image"
	"math"
)

// Status of a tile after alignment.
const (
	Placed   = "placed"
	Rejected = "rejected"
)

// Options tune Align. Zero values select the defaults.
type Options struct {
	// MinMatch is the lowest normalized cross-correlation (at the image's
	// own resolution) at which a tile is accepted. Default 0.5.
	MinMatch float64
	// MaxScale is the largest accepted deviation from the expected scale
	// (fraction). Default 0.08.
	MaxScale float64
	// MaxShift is the largest accepted shift, as a fraction of the tile.
	// Default 0.1.
	MaxShift float64
	// NoColorMatch skips the low-frequency color correction.
	NoColorMatch bool
}

func (o Options) withDefaults() Options {
	if o.MinMatch == 0 {
		o.MinMatch = 0.5
	}
	if o.MaxScale == 0 {
		o.MaxScale = 0.08
	}
	if o.MaxShift == 0 {
		o.MaxShift = 0.1
	}
	return o
}

// Alignment maps a returned tile onto its crop box: the tile is fitted to
// the box, scaled by ScaleX/ScaleY about the box center, then shifted by
// ShiftX/ShiftY (plan-image pixels).
type Alignment struct {
	Status string  `json:"status"`
	ScaleX float64 `json:"scaleX"`
	ScaleY float64 `json:"scaleY"`
	ShiftX float64 `json:"shiftX"`
	ShiftY float64 `json:"shiftY"`
	// Match is the normalized cross-correlation between the aligned tile and
	// the image (1 = identical structure).
	Match float64 `json:"match"`
	// WorstBlock is the lowest match of the 3x3 blocks of the tile that have
	// enough detail to judge; a low value means part of the tile differs.
	WorstBlock float64 `json:"worstBlock"`
	// Flat is set when the area has too little detail to align against; the
	// tile is then placed at its nominal position.
	Flat bool   `json:"flat,omitempty"`
	Note string `json:"note,omitempty"`
	// Color is the low-frequency color correction (nil when disabled).
	Color *ColorGrid `json:"-"`
}

// planes is an RGB image as float32 planes in 0-255, plus blurred luma.
type planes struct {
	w, h    int
	r, g, b []float32
	y       []float32
}

func toPlanes(img *image.RGBA) *planes {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	n := w * h
	p := &planes{w: w, h: h, r: make([]float32, n), g: make([]float32, n), b: make([]float32, n), y: make([]float32, n)}
	for yy := range h {
		row := img.Pix[yy*img.Stride:]
		for x := range w {
			i := yy*w + x
			r, g, b := float32(row[4*x]), float32(row[4*x+1]), float32(row[4*x+2])
			p.r[i], p.g[i], p.b[i] = r, g, b
			p.y[i] = 0.299*r + 0.587*g + 0.114*b
		}
	}
	p.y = blur(p.y, w, h)
	return p
}

// blur applies a separable [1 2 1]/4 kernel, clamping at the edges.
func blur(in []float32, w, h int) []float32 {
	tmp := make([]float32, len(in))
	for y := range h {
		row := in[y*w : (y+1)*w]
		for x := range w {
			l, r := row[max(x-1, 0)], row[min(x+1, w-1)]
			tmp[y*w+x] = (l + 2*row[x] + r) / 4
		}
	}
	out := make([]float32, len(in))
	for y := range h {
		up, dn := max(y-1, 0), min(y+1, h-1)
		for x := range w {
			out[y*w+x] = (tmp[up*w+x] + 2*tmp[y*w+x] + tmp[dn*w+x]) / 4
		}
	}
	return out
}

// sample bilinearly interpolates c at continuous coordinates (pixel centers
// at i+0.5). ok is false outside the plane.
func (p *planes) sample(c []float32, x, y float64) (float32, bool) {
	fx, fy := x-0.5, y-0.5
	if fx < 0 || fy < 0 || fx > float64(p.w-1) || fy > float64(p.h-1) {
		return 0, false
	}
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, p.w-1), min(y0+1, p.h-1)
	ax, ay := float32(fx-float64(x0)), float32(fy-float64(y0))
	top := c[y0*p.w+x0]*(1-ax) + c[y0*p.w+x1]*ax
	bot := c[y1*p.w+x0]*(1-ax) + c[y1*p.w+x1]*ax
	return top*(1-ay) + bot*ay, true
}

// params is a candidate alignment in plan-image pixels.
type params struct{ sx, sy, dx, dy float64 }

// level is one resolution of the search.
type level struct {
	k        float64 // level pixels per plan pixel (nominal)
	win, box Box     // plan pixels
	base     *planes // the window of the plan image
	tile     *planes // the tile fitted to the box
	bsx, bsy float64 // actual base plane scale (plane px per plan px)
	tsx, tsy float64 // actual tile plane scale
	// The resampled images the planes came from.
	baseImg, tileImg *image.RGBA
}

// newLevel resamples the window of the plan image and the tile to scale k
// (level pixels per plan pixel). base and tile are the finest renditions
// available: the originals, or the finest level for coarser ones.
func newLevel(base *image.RGBA, win Box, tile *image.RGBA, box Box, k float64) *level {
	bw, bh := max(int(math.Round(float64(win.W)*k)), 2), max(int(math.Round(float64(win.H)*k)), 2)
	tw, th := max(int(math.Round(float64(box.W)*k)), 2), max(int(math.Round(float64(box.H)*k)), 2)
	bi, ti := base, tile
	if bi.Rect.Dx() != bw || bi.Rect.Dy() != bh {
		bi = Resize(base, base.Rect, bw, bh)
	}
	if ti.Rect.Dx() != tw || ti.Rect.Dy() != th {
		ti = Resize(tile, tile.Rect, tw, th)
	}
	l := &level{k: k, win: win, box: box, base: toPlanes(bi), tile: toPlanes(ti), baseImg: bi, tileImg: ti}
	l.bsx, l.bsy = float64(bw)/float64(win.W), float64(bh)/float64(win.H)
	l.tsx, l.tsy = float64(tw)/float64(box.W), float64(th)/float64(box.H)
	return l
}

// rect is a plan-pixel rectangle with float edges.
type rect struct{ x0, y0, x1, y1 float64 }

// span returns the base-plane pixel range covering r.
func (l *level) span(r rect) (x0, y0, x1, y1 int) {
	x0 = max(int(math.Floor((r.x0-float64(l.win.X))*l.bsx)), 0)
	y0 = max(int(math.Floor((r.y0-float64(l.win.Y))*l.bsy)), 0)
	x1 = min(int(math.Ceil((r.x1-float64(l.win.X))*l.bsx)), l.base.w)
	y1 = min(int(math.Ceil((r.y1-float64(l.win.Y))*l.bsy)), l.base.h)
	return
}

// tileCoord maps base-plane pixel (x, y) to tile-plane coordinates under p:
// u = ua + x*ub, v = va + y*vb.
func (l *level) tileCoord(p params) (ua, ub, va, vb float64) {
	cx := float64(l.box.X) + float64(l.box.W)/2
	cy := float64(l.box.Y) + float64(l.box.H)/2
	// plan x of base pixel center: win.X + (x+0.5)/bsx; nominal tile x:
	// cx + (px-cx-dx)/sx; tile plane: (nx-box.X)*tsx.
	ub = l.tsx / (l.bsx * p.sx)
	ua = (cx + (float64(l.win.X)+0.5/l.bsx-cx-p.dx)/p.sx - float64(l.box.X)) * l.tsx
	vb = l.tsy / (l.bsy * p.sy)
	va = (cy + (float64(l.win.Y)+0.5/l.bsy-cy-p.dy)/p.sy - float64(l.box.Y)) * l.tsy
	return
}

// ncc is the normalized cross-correlation of base and tile luma over r
// under p; it returns -1 when less than half of r is covered by the tile.
func (l *level) ncc(p params, r rect) float64 {
	x0, y0, x1, y1 := l.span(r)
	total := (x1 - x0) * (y1 - y0)
	if total <= 0 {
		return -1
	}
	ua, ub, va, vb := l.tileCoord(p)
	var sa, sb, saa, sbb, sab float64
	n := 0
	for y := y0; y < y1; y++ {
		v := va + float64(y)*vb
		row := l.base.y[y*l.base.w:]
		for x := x0; x < x1; x++ {
			t, ok := l.tile.sample(l.tile.y, ua+float64(x)*ub, v)
			if !ok {
				continue
			}
			a, b := float64(row[x]), float64(t)
			sa += a
			sb += b
			saa += a * a
			sbb += b * b
			sab += a * b
			n++
		}
	}
	if n < total/2 {
		return -1
	}
	fn := float64(n)
	va2 := saa - sa*sa/fn
	vb2 := sbb - sb*sb/fn
	if va2 <= 1e-6 || vb2 <= 1e-6 {
		return 0
	}
	return (sab - sa*sb/fn) / math.Sqrt(va2*vb2)
}

// detail is the standard deviation of base luma over r.
func (l *level) detail(r rect) float64 {
	x0, y0, x1, y1 := l.span(r)
	var s, ss float64
	n := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			v := float64(l.base.y[y*l.base.w+x])
			s += v
			ss += v * v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	m := s / float64(n)
	return math.Sqrt(math.Max(ss/float64(n)-m*m, 0))
}

// maxWorkPixels bounds the long side of the finest search level: the tile is
// compared with the image at (at most) the image's own resolution.
const maxWorkPixels = 768

// minLevelPixels is the long side of the coarsest search level.
const minLevelPixels = 40

// Align registers tile against the area box of base (the plan image) and
// returns where it belongs. The tile is expected to show the crop at box,
// re-rendered at any resolution with the same aspect ratio.
func Align(base image.Image, box Box, tile image.Image, opt Options) Alignment {
	opt = opt.withDefaults()
	ident := Alignment{ScaleX: 1, ScaleY: 1}
	tb := tile.Bounds()
	if tb.Dx() < 2 || tb.Dy() < 2 {
		ident.Status, ident.Note = Rejected, "the returned image is empty"
		return ident
	}
	if d := math.Abs(math.Log(float64(tb.Dx()) / float64(tb.Dy()) / box.ratio())); d > 0.08 {
		ident.Status = Rejected
		ident.Note = fmt.Sprintf("the returned image is %dx%d, a different shape from the %dx%d crop; request the crop's aspectRatio", tb.Dx(), tb.Dy(), box.W, box.H)
		return ident
	}

	pb := base.Bounds()
	mx, my := int(math.Ceil(0.12*float64(box.W))), int(math.Ceil(0.12*float64(box.H)))
	win := Box{X: max(box.X-mx, 0), Y: max(box.Y-my, 0)}
	win.W = min(box.X1()+mx, pb.Dx()) - win.X
	win.H = min(box.Y1()+my, pb.Dy()) - win.Y

	long := float64(max(box.W, box.H))
	k0 := math.Min(1, maxWorkPixels/long)
	o := pb.Min
	winImg := asRGBA(base, image.Rect(win.X, win.Y, win.X1(), win.Y1()).Add(o))
	levels := []*level{newLevel(winImg, win, asRGBA(tile, tb), box, k0)} // finest first
	for k := k0 / 2; long*k >= minLevelPixels; k /= 2 {
		levels = append(levels, newLevel(levels[0].baseImg, win, levels[0].tileImg, box, k))
	}

	// Compare inside the box, away from its edges (models soften borders).
	ev := rect{
		x0: float64(box.X) + 0.08*float64(box.W), y0: float64(box.Y) + 0.08*float64(box.H),
		x1: float64(box.X1()) - 0.08*float64(box.W), y1: float64(box.Y1()) - 0.08*float64(box.H),
	}
	fine := levels[0]
	if fine.detail(ev) < 3 {
		ident.Status, ident.Flat = Placed, true
		ident.Match = fine.ncc(params{1, 1, 0, 0}, ev)
		ident.WorstBlock = ident.Match
		ident.Note = "too little detail to align against; placed at the crop position"
		if !opt.NoColorMatch {
			ident.Color = colorGrid(fine, params{1, 1, 0, 0})
		}
		return ident
	}

	// Coarse exhaustive search over uniform scale and shift.
	coarse := levels[len(levels)-1]
	best, bestScore := params{1, 1, 0, 0}, math.Inf(-1)
	step := 1 / coarse.k
	rx := math.Floor(opt.MaxShift * float64(box.W) / step)
	ry := math.Floor(opt.MaxShift * float64(box.H) / step)
	for s := 1 - opt.MaxScale; s <= 1+opt.MaxScale+1e-9; s += 0.02 {
		for iy := -ry; iy <= ry; iy++ {
			for ix := -rx; ix <= rx; ix++ {
				p := params{s, s, ix * step, iy * step}
				if sc := coarse.ncc(p, ev); sc > bestScore {
					best, bestScore = p, sc
				}
			}
		}
	}
	// Refine, level by level, including independent x/y scale.
	for i := len(levels) - 1; i >= 0; i-- {
		best, bestScore = refine(levels[i], best, ev, box, opt)
	}

	a := Alignment{ScaleX: best.sx, ScaleY: best.sy, ShiftX: best.dx, ShiftY: best.dy, Match: bestScore}
	a.WorstBlock = worstBlock(fine, best, ev)
	switch {
	case bestScore < opt.MinMatch:
		a.Status = Rejected
		a.Note = fmt.Sprintf("the tile does not match the image here (match %.2f); the model probably changed the content or framing", bestScore)
	case math.Abs(best.sx-1) > opt.MaxScale || math.Abs(best.sy-1) > opt.MaxScale ||
		math.Abs(best.dx) > opt.MaxShift*float64(box.W) || math.Abs(best.dy) > opt.MaxShift*float64(box.H):
		a.Status = Rejected
		a.Note = fmt.Sprintf("the tile is shifted or zoomed beyond the accepted range (scale %.3f x %.3f, shift %.0f,%.0f px)", best.sx, best.sy, best.dx, best.dy)
	default:
		a.Status = Placed
		if a.WorstBlock < 0.3 {
			a.Note = fmt.Sprintf("part of the tile differs from the image (worst block match %.2f); inspect it", a.WorstBlock)
		}
		if !opt.NoColorMatch {
			a.Color = colorGrid(fine, best)
		}
	}
	return a
}

// refine improves p on level l by pattern search with shrinking steps.
func refine(l *level, p params, ev rect, box Box, opt Options) (params, float64) {
	best := l.ncc(p, ev)
	dstep := 1 / l.k
	sstep := 1 / (float64(max(box.W, box.H)) * l.k)
	lim := func(q params) bool {
		return math.Abs(q.sx-1) <= opt.MaxScale*1.5 && math.Abs(q.sy-1) <= opt.MaxScale*1.5 &&
			math.Abs(q.dx) <= opt.MaxShift*1.5*float64(box.W) && math.Abs(q.dy) <= opt.MaxShift*1.5*float64(box.H)
	}
	for range 3 { // step sizes 1, 1/2, 1/4 of a level pixel
		for iter := 0; iter < 40; iter++ {
			improved := false
			for _, q := range []params{
				{p.sx, p.sy, p.dx + dstep, p.dy}, {p.sx, p.sy, p.dx - dstep, p.dy},
				{p.sx, p.sy, p.dx, p.dy + dstep}, {p.sx, p.sy, p.dx, p.dy - dstep},
				{p.sx + sstep, p.sy, p.dx, p.dy}, {p.sx - sstep, p.sy, p.dx, p.dy},
				{p.sx, p.sy + sstep, p.dx, p.dy}, {p.sx, p.sy - sstep, p.dx, p.dy},
				{p.sx + sstep, p.sy + sstep, p.dx, p.dy}, {p.sx - sstep, p.sy - sstep, p.dx, p.dy},
			} {
				if !lim(q) {
					continue
				}
				if sc := l.ncc(q, ev); sc > best {
					p, best, improved = q, sc, true
				}
			}
			if !improved {
				break
			}
		}
		dstep /= 2
		sstep /= 2
	}
	return p, best
}

// worstBlock is the lowest match among the 3x3 blocks of ev that have
// enough detail to judge (1 when none has).
func worstBlock(l *level, p params, ev rect) float64 {
	worst := 1.0
	bw, bh := (ev.x1-ev.x0)/3, (ev.y1-ev.y0)/3
	for j := range 3 {
		for i := range 3 {
			r := rect{ev.x0 + float64(i)*bw, ev.y0 + float64(j)*bh, ev.x0 + float64(i+1)*bw, ev.y0 + float64(j+1)*bh}
			if l.detail(r) < 4 {
				continue
			}
			worst = math.Min(worst, l.ncc(p, r))
		}
	}
	return worst
}

// ColorGrid is a smooth per-channel offset over the crop box that moves the
// tile's broad exposure and color onto the image's, without touching fine
// texture. Cells are laid out over the box in normalized coordinates.
type ColorGrid struct {
	GX, GY int
	Off    [][3]float32
}

// At returns the offset at normalized box coordinates (u, v), bilinearly
// interpolated between cell centers.
func (g *ColorGrid) At(u, v float64) [3]float32 {
	if g == nil {
		return [3]float32{}
	}
	fx := math.Min(math.Max(u*float64(g.GX)-0.5, 0), float64(g.GX-1))
	fy := math.Min(math.Max(v*float64(g.GY)-0.5, 0), float64(g.GY-1))
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, g.GX-1), min(y0+1, g.GY-1)
	ax, ay := float32(fx-float64(x0)), float32(fy-float64(y0))
	var out [3]float32
	for c := range 3 {
		top := g.Off[y0*g.GX+x0][c]*(1-ax) + g.Off[y0*g.GX+x1][c]*ax
		bot := g.Off[y1*g.GX+x0][c]*(1-ax) + g.Off[y1*g.GX+x1][c]*ax
		out[c] = top*(1-ay) + bot*ay
	}
	return out
}

// maxColorOffset caps the correction so a genuinely different tile is not
// repainted into the image's colors.
const maxColorOffset = 48

func colorGrid(l *level, p params) *ColorGrid {
	box := l.box
	gx, gy := 8, 8
	if box.W >= box.H {
		gy = max(2, int(math.Round(8*float64(box.H)/float64(box.W))))
	} else {
		gx = max(2, int(math.Round(8*float64(box.W)/float64(box.H))))
	}
	type acc struct {
		base, tile [3]float64
		n          int
	}
	cells := make([]acc, gx*gy)
	r := rect{float64(box.X), float64(box.Y), float64(box.X1()), float64(box.Y1())}
	x0, y0, x1, y1 := l.span(r)
	ua, ub, va, vb := l.tileCoord(p)
	for y := y0; y < y1; y++ {
		py := float64(l.win.Y) + (float64(y)+0.5)/l.bsy
		cy := int((py - float64(box.Y)) / float64(box.H) * float64(gy))
		if cy < 0 || cy >= gy {
			continue
		}
		v := va + float64(y)*vb
		for x := x0; x < x1; x++ {
			px := float64(l.win.X) + (float64(x)+0.5)/l.bsx
			cx := int((px - float64(box.X)) / float64(box.W) * float64(gx))
			if cx < 0 || cx >= gx {
				continue
			}
			u := ua + float64(x)*ub
			tr, ok := l.tile.sample(l.tile.r, u, v)
			if !ok {
				continue
			}
			tg, _ := l.tile.sample(l.tile.g, u, v)
			tbl, _ := l.tile.sample(l.tile.b, u, v)
			i := y*l.base.w + x
			c := &cells[cy*gx+cx]
			c.base[0] += float64(l.base.r[i])
			c.base[1] += float64(l.base.g[i])
			c.base[2] += float64(l.base.b[i])
			c.tile[0] += float64(tr)
			c.tile[1] += float64(tg)
			c.tile[2] += float64(tbl)
			c.n++
		}
	}
	expect := (x1 - x0) * (y1 - y0) / (gx * gy)
	raw := make([][3]float32, gx*gy)
	valid := make([]bool, gx*gy)
	for i, c := range cells {
		if c.n == 0 || c.n < expect/4 {
			continue
		}
		valid[i] = true
		for ch := range 3 {
			raw[i][ch] = float32((c.base[ch] - c.tile[ch]) / float64(c.n))
		}
	}
	// Smooth over valid neighbors (also fills cells without samples).
	g := &ColorGrid{GX: gx, GY: gy, Off: make([][3]float32, gx*gy)}
	for j := range gy {
		for i := range gx {
			var sum [3]float32
			var wsum float32
			for dj := -1; dj <= 1; dj++ {
				for di := -1; di <= 1; di++ {
					ii, jj := i+di, j+dj
					if ii < 0 || jj < 0 || ii >= gx || jj >= gy || !valid[jj*gx+ii] {
						continue
					}
					w := float32(1)
					if di == 0 && dj == 0 {
						w = 2
					}
					for ch := range 3 {
						sum[ch] += raw[jj*gx+ii][ch] * w
					}
					wsum += w
				}
			}
			if wsum == 0 {
				continue
			}
			for ch := range 3 {
				g.Off[j*gx+i][ch] = float32(math.Max(-maxColorOffset, math.Min(maxColorOffset, float64(sum[ch]/wsum))))
			}
		}
	}
	return g
}
