package tiles

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"

	"golang.org/x/image/draw"
)

var nb2Ratios = ParseRatios([]string{"1:1", "1:4", "1:8", "2:3", "3:2", "3:4", "4:1", "4:3", "4:5", "5:4", "8:1", "9:16", "16:9", "21:9"})

func ratioOf(t *testing.T, name string) float64 {
	t.Helper()
	for _, r := range nb2Ratios {
		if r.Name == name {
			return r.Value
		}
	}
	t.Fatalf("unknown ratio %q", name)
	return 0
}

func TestGridCoversImageWithSupportedRatios(t *testing.T) {
	for _, size := range [][2]int{{1200, 800}, {1000, 1000}, {640, 1138}, {3000, 1000}, {517, 389}} {
		w, h := size[0], size[1]
		ts, err := Grid(w, h, 3, 0.2, nb2Ratios)
		if err != nil {
			t.Fatal(err)
		}
		if len(ts) != 9 {
			t.Fatalf("%dx%d: %d tiles", w, h, len(ts))
		}
		covered := make([]bool, w*h)
		for _, tl := range ts {
			b := tl.Box
			if b.X < 0 || b.Y < 0 || b.X1() > w || b.Y1() > h {
				t.Fatalf("%dx%d tile %s: box %+v outside the image", w, h, tl.Label, b)
			}
			if !b.contains(tl.Core) {
				t.Fatalf("%dx%d tile %s: box %+v misses core %+v", w, h, tl.Label, b, tl.Core)
			}
			if tl.Outside {
				t.Fatalf("%dx%d tile %s: extends past the image", w, h, tl.Label)
			}
			// Within a pixel of the named ratio.
			want := ratioOf(t, tl.AspectRatio)
			if math.Abs(float64(b.W)-want*float64(b.H)) > 1 && math.Abs(float64(b.H)-float64(b.W)/want) > 1 {
				t.Fatalf("%dx%d tile %s: %dx%d is not %s", w, h, tl.Label, b.W, b.H, tl.AspectRatio)
			}
			for y := tl.Core.Y; y < tl.Core.Y1(); y++ {
				for x := tl.Core.X; x < tl.Core.X1(); x++ {
					covered[y*w+x] = true
				}
			}
		}
		for i, c := range covered {
			if !c {
				t.Fatalf("%dx%d: pixel %d,%d not in any core", w, h, i%w, i/w)
			}
		}
	}
}

func TestGridRejectsTinyImages(t *testing.T) {
	if _, err := Grid(30, 30, 3, 0.2, nb2Ratios); err == nil {
		t.Fatal("expected an error for a 30x30 image")
	}
}

func TestSnapShrinksWhenGrowingCannotFit(t *testing.T) {
	// The padded region fills a 2:1 image, which no ratio can grow inside;
	// shrinking to 16:9 still keeps the region itself.
	ts, err := Regions(2000, 1000, []Region{{X: 0.2, Y: 0.2, W: 0.6, H: 0.6, Label: "middle"}}, 0.5, nb2Ratios)
	if err != nil {
		t.Fatal(err)
	}
	tl := ts[0]
	if tl.Outside || tl.AspectRatio != "16:9" || tl.Box.H != 1000 || !tl.Box.contains(tl.Core) {
		t.Fatalf("tile = %+v", tl)
	}
	// When even shrinking cannot keep the region, the tile grows past the
	// border with the least added area: 16:9 adds 63 rows above and below.
	ts, _ = Regions(2000, 1000, []Region{{X: 0, Y: 0, W: 1, H: 1}}, 0.2, nb2Ratios)
	if !ts[0].Outside || ts[0].Box != (Box{0, -62, 2000, 1125}) || ts[0].AspectRatio != "16:9" {
		t.Fatalf("tile = %+v", ts[0])
	}
	if ts, err := Grid(2000, 1000, 1, 0.2, nb2Ratios); err != nil || ts[0].Box != (Box{0, -62, 2000, 1125}) {
		t.Fatalf("1x1 grid = %+v %v", ts, err)
	}
}

func TestRegions(t *testing.T) {
	ts, err := Regions(1000, 800, []Region{{X: 0.4, Y: 0.1, W: 0.2, H: 0.3, Label: "face"}}, 0.2, nb2Ratios)
	if err != nil {
		t.Fatal(err)
	}
	tl := ts[0]
	if tl.Label != "face" || tl.Core != (Box{400, 80, 200, 240}) || !tl.Box.contains(tl.Core) {
		t.Fatalf("tile = %+v", tl)
	}
	if _, err := Regions(1000, 800, []Region{{X: 0.5, Y: 0.5, W: 0.001, H: 0.2}}, 0.2, nb2Ratios); err == nil {
		t.Fatal("expected an error for a sliver region")
	}
}

func TestParseRatiosSkipsJunk(t *testing.T) {
	rs := ParseRatios([]string{"16:9", "x", "0:1", "3:2"})
	if len(rs) != 2 || rs[0].Name != "16:9" || math.Abs(rs[1].Value-1.5) > 1e-9 {
		t.Fatalf("ratios = %+v", rs)
	}
}

// truth renders a deterministic textured scene: smooth color fields, edges
// and fine noise, so alignment has structure at every scale.
func truth(w, h int, seed uint64) *image.RGBA {
	rng := rand.New(rand.NewPCG(seed, 1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	type blob struct{ x, y, r float64 }
	var blobs []blob
	for range 40 {
		blobs = append(blobs, blob{rng.Float64() * float64(w), rng.Float64() * float64(h), 10 + rng.Float64()*float64(w)/12})
	}
	for y := range h {
		for x := range w {
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			r := 120 + 60*math.Sin(fx*7+fy*3)
			g := 110 + 50*math.Cos(fx*5-fy*6)
			b := 100 + 40*math.Sin(fx*11*fy+2)
			for i, bl := range blobs {
				if dx, dy := float64(x)-bl.x, float64(y)-bl.y; dx*dx+dy*dy < bl.r*bl.r {
					r += float64(30 * (i%3 - 1))
					g += float64(25 * ((i+1)%3 - 1))
					b += float64(20 * ((i+2)%3 - 1))
				}
			}
			n := (rng.Float64() - 0.5) * 30
			img.Set(x, y, color.RGBA{clamp8(r + n), clamp8(g + n), clamp8(b + n), 255})
		}
	}
	return img
}

func clamp8(v float64) uint8 { return uint8(math.Max(0, math.Min(255, math.Round(v)))) }

func scaled(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst
}

// fakeTile renders what an ideal model returns for box (plan pixels): the
// truth image at that place, at tw x th, but drifted by scale s and shift
// (dx, dy) plan pixels, and with a color cast.
func fakeTile(g *image.RGBA, k float64, box Box, s, dx, dy float64, tw, th int, cast int) *image.RGBA {
	cx := (float64(box.X) + float64(box.W)/2 + dx) * k
	cy := (float64(box.Y) + float64(box.H)/2 + dy) * k
	hw, hh := s*float64(box.W)*k/2, s*float64(box.H)*k/2
	r := image.Rect(int(math.Round(cx-hw)), int(math.Round(cy-hh)), int(math.Round(cx+hw)), int(math.Round(cy+hh)))
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.CatmullRom.Scale(dst, dst.Bounds(), g, r, draw.Src, nil)
	if cast != 0 {
		for i := 0; i < len(dst.Pix); i += 4 {
			dst.Pix[i] = clamp8(float64(dst.Pix[i]) + float64(cast))
			dst.Pix[i+2] = clamp8(float64(dst.Pix[i+2]) - float64(cast))
		}
	}
	return dst
}

func TestAlignRecoversDrift(t *testing.T) {
	const k = 4 // truth is 4x the plan image
	g := truth(1024, 768, 1)
	plan := scaled(g, 256, 192)
	box := Box{X: 72, Y: 48, W: 112, H: 84} // 4:3
	for _, c := range []struct{ s, dx, dy float64 }{
		{1, 0, 0}, {1.03, 3.5, -2.25}, {0.96, -5, 4}, {1.0, 0.4, 0.3},
	} {
		tile := fakeTile(g, k, box, c.s, c.dx, c.dy, 840, 630, 0)
		a := Align(plan, box, tile, Options{})
		if a.Status != Placed {
			t.Fatalf("%+v: status %s (%s)", c, a.Status, a.Note)
		}
		if math.Abs(a.ScaleX-c.s) > 0.006 || math.Abs(a.ScaleY-c.s) > 0.006 ||
			math.Abs(a.ShiftX-c.dx) > 0.35 || math.Abs(a.ShiftY-c.dy) > 0.35 {
			t.Fatalf("%+v: got scale %.4f x %.4f shift %.2f,%.2f", c, a.ScaleX, a.ScaleY, a.ShiftX, a.ShiftY)
		}
		if a.Match < 0.9 {
			t.Fatalf("%+v: match %.3f", c, a.Match)
		}
	}
}

func TestAlignRejectsUnrelatedOrMisshapenTiles(t *testing.T) {
	g := truth(1024, 768, 2)
	plan := scaled(g, 256, 192)
	box := Box{X: 64, Y: 60, W: 112, H: 84}
	other := truth(840, 630, 99)
	if a := Align(plan, box, other, Options{}); a.Status != Rejected {
		t.Fatalf("unrelated tile: %+v", a)
	}
	wide := fakeTile(g, 4, box, 1, 0, 0, 1120, 630, 0)
	if a := Align(plan, box, wide, Options{}); a.Status != Rejected || a.Note == "" {
		t.Fatalf("16:9 tile for a 4:3 crop: %+v", a)
	}
}

func TestAlignFlatAreaPlacesNominally(t *testing.T) {
	plan := image.NewRGBA(image.Rect(0, 0, 400, 300))
	draw.Draw(plan, plan.Bounds(), image.NewUniform(color.RGBA{90, 120, 200, 255}), image.Point{}, draw.Src)
	tile := image.NewRGBA(image.Rect(0, 0, 800, 600))
	draw.Draw(tile, tile.Bounds(), image.NewUniform(color.RGBA{100, 120, 190, 255}), image.Point{}, draw.Src)
	a := Align(plan, Box{100, 100, 160, 120}, tile, Options{})
	if a.Status != Placed || !a.Flat || a.ShiftX != 0 || a.ScaleX != 1 {
		t.Fatalf("flat: %+v", a)
	}
	off := a.Color.At(0.5, 0.5)
	if math.Abs(float64(off[0]+10)) > 1 || math.Abs(float64(off[2]-10)) > 1 {
		t.Fatalf("color offset = %v, want about -10, 0, +10", off)
	}
}

func TestFeathersLimitToHalfTheOverlap(t *testing.T) {
	ts, _ := Grid(900, 600, 3, 0.2, nb2Ratios)
	ps := make([]Placement, len(ts))
	for i, tl := range ts {
		ps[i] = Placement{Box: tl.Box}
	}
	Feathers(ps, 900, 600, 0.2)
	c := ps[4] // center tile: no border sides
	if c.Border != [4]bool{} {
		t.Fatalf("center borders = %v", c.Border)
	}
	left := ps[3].Box
	if want := float64(left.X1()-c.Box.X) / 2; c.Feather[0] > want+1e-9 || c.Feather[0] <= 0 {
		t.Fatalf("center left feather %.1f, overlap/2 %.1f", c.Feather[0], want)
	}
	corner := ps[0]
	if corner.Border != [4]bool{true, true, false, false} || corner.Feather[0] != 0 || corner.Feather[2] <= 0 {
		t.Fatalf("corner = %+v", corner)
	}
}

// Overlaps are measured where the tiles were aligned: a neighbor shifted
// away leaves less overlap, and the feathers shrink with it.
func TestFeathersFollowAlignment(t *testing.T) {
	a := Placement{Box: Box{X: 0, Y: 0, W: 100, H: 100}, Align: Alignment{ScaleX: 1, ScaleY: 1}}
	b := Placement{Box: Box{X: 80, Y: 0, W: 100, H: 100}, Align: Alignment{ScaleX: 1, ScaleY: 1, ShiftX: 12}}
	ps := []Placement{a, b}
	Feathers(ps, 180, 100, 0.2)
	// 20 px apart as cut, 8 px as aligned: each side fades over at most 4.
	if ps[0].Feather[2] > 4+1e-9 || ps[1].Feather[0] > 4+1e-9 || ps[0].Feather[2] <= 0 {
		t.Fatalf("feathers %v / %v", ps[0].Feather, ps[1].Feather)
	}
}

// assemble runs the whole stitch on synthetic tiles and returns the canvas.
func assemble(t *testing.T, g, plan *image.RGBA, ts []Tile, k float64, skip map[int]bool, drift func(i int) (s, dx, dy float64)) *Canvas {
	t.Helper()
	var ps []Placement
	var tilesImg []image.Image
	for i, tl := range ts {
		if skip[i] {
			continue
		}
		s, dx, dy := drift(i)
		tw := int(float64(tl.Box.W) * k * 1.3)
		th := int(math.Round(float64(tw) * float64(tl.Box.H) / float64(tl.Box.W)))
		tile := fakeTile(g, k, tl.Box, s, dx, dy, tw, th, (i%3-1)*6)
		a := Align(plan, tl.Box, tile, Options{})
		if a.Status != Placed {
			t.Fatalf("tile %s: %s (%s)", tl.Label, a.Status, a.Note)
		}
		ps = append(ps, Placement{Box: tl.Box, Align: a})
		tilesImg = append(tilesImg, tile)
	}
	pb := plan.Bounds()
	Feathers(ps, pb.Dx(), pb.Dy(), 0.2)
	c := NewCanvas(plan, g.Bounds().Dx(), g.Bounds().Dy())
	c.Reserve(ps)
	for i, p := range ps {
		c.Paint(c.Prepare(p, tilesImg[i]))
	}
	return c
}

// meanAbsDiff compares two same-size images over r (luma).
func meanAbsDiff(a, b *image.RGBA, r image.Rectangle) float64 {
	var sum float64
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			pa, pb := a.RGBAAt(x, y), b.RGBAAt(x, y)
			la := 0.299*float64(pa.R) + 0.587*float64(pa.G) + 0.114*float64(pa.B)
			lb := 0.299*float64(pb.R) + 0.587*float64(pb.G) + 0.114*float64(pb.B)
			sum += math.Abs(la - lb)
			n++
		}
	}
	return sum / float64(n)
}

func TestStitchGridMatchesTruthWithoutSeams(t *testing.T) {
	const k = 4
	g := truth(1024, 768, 3)
	plan := scaled(g, 256, 192)
	ts, err := Grid(256, 192, 3, 0.2, nb2Ratios)
	if err != nil {
		t.Fatal(err)
	}
	drifts := [][3]float64{{1.02, 1.5, -1}, {0.98, -1.5, 1.5}, {1, 0.5, 0.5}, {1.01, -1.5, -1.5}, {1, 0, 0}, {0.99, 1, 2}, {1.03, 2, 1}, {1, -1, 1}, {0.97, 1.5, -1.5}}
	c := assemble(t, g, plan, ts, k, nil, func(i int) (float64, float64, float64) {
		return drifts[i][0], drifts[i][1], drifts[i][2]
	})
	baseline := scaled(plan, 1024, 768) // plain interpolation of the plan image
	all := g.Bounds()
	got, base := meanAbsDiff(c.Img, g, all), meanAbsDiff(baseline, g, all)
	t.Logf("mean abs luma error: stitched %.2f, interpolated %.2f", got, base)
	if got > base*0.7 {
		t.Fatalf("stitched error %.2f is not clearly below interpolation %.2f", got, base)
	}
	// Seams: the error along the interior grid lines is no worse than the
	// error in tile interiors (no doubling, no color steps).
	interior := meanAbsDiff(c.Img, g, image.Rect(120, 90, 220, 160))
	for _, x := range []int{341, 683} {
		seam := meanAbsDiff(c.Img, g, image.Rect(x-10, 60, x+10, 700))
		t.Logf("seam x=%d error %.2f, interior %.2f", x, seam, interior)
		if seam > interior*1.5+1 {
			t.Fatalf("seam at x=%d: error %.2f vs interior %.2f", x, seam, interior)
		}
	}
}

func TestStitchFillsRejectedTileFromBase(t *testing.T) {
	const k = 4
	g := truth(1024, 768, 4)
	plan := scaled(g, 256, 192)
	ts, _ := Grid(256, 192, 3, 0.2, nb2Ratios)
	c := assemble(t, g, plan, ts, k, map[int]bool{4: true}, func(int) (float64, float64, float64) { return 1, 0, 0 })
	// The center core keeps the (interpolated) base; nothing is left black.
	baseline := scaled(plan, 1024, 768)
	center := image.Rect(400, 300, 624, 468)
	if d := meanAbsDiff(c.Img, baseline, center); d > 1 {
		t.Fatalf("center differs from the base by %.2f", d)
	}
}

func TestRegionPaintsOverBaseWithFeather(t *testing.T) {
	g := truth(1024, 768, 5)
	plan := scaled(g, 1024, 768) // later pass: the plan image is the canvas
	ts, _ := Regions(1024, 768, []Region{{X: 0.3, Y: 0.3, W: 0.2, H: 0.2, Label: "detail"}}, 0.2, nb2Ratios)
	tl := ts[0]
	// A tile that is brighter everywhere: color matching should pull it back.
	tile := fakeTile(g, 1, tl.Box, 1, 0, 0, tl.Box.W*2, tl.Box.H*2, 0)
	for i := 0; i < len(tile.Pix); i += 4 {
		tile.Pix[i+1] = clamp8(float64(tile.Pix[i+1]) + 30)
	}
	a := Align(plan, tl.Box, tile, Options{})
	if a.Status != Placed {
		t.Fatalf("status %s (%s)", a.Status, a.Note)
	}
	ps := []Placement{{Box: tl.Box, Align: a}}
	Feathers(ps, 1024, 768, 0.2)
	before := image.NewRGBA(plan.Bounds())
	copy(before.Pix, plan.Pix)
	c := NewCanvas(plan, 1024, 768)
	c.Reserve(ps)
	c.Paint(c.Prepare(ps[0], tile))
	// Outside the box nothing changes; inside, color stays close to the image.
	if d := meanAbsDiff(c.Img, before, image.Rect(0, 0, 1024, tl.Box.Y-2)); d != 0 {
		t.Fatalf("pixels above the region changed by %.2f", d)
	}
	if d := meanAbsDiff(c.Img, before, image.Rect(tl.Box.X+30, tl.Box.Y+30, tl.Box.X1()-30, tl.Box.Y1()-30)); d > 4 {
		t.Fatalf("region interior differs by %.2f after color matching", d)
	}
}

func TestCropMirrorsOutsideTheImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for x := range 4 {
		img.SetRGBA(x, 0, color.RGBA{uint8(10 * x), 0, 0, 255})
		img.SetRGBA(x, 1, color.RGBA{uint8(10 * x), 100, 0, 255})
	}
	c := Crop(img, Box{X: -2, Y: -1, W: 8, H: 4})
	// Columns -2,-1 mirror to 1,0; 4,5 mirror to 3,2. Row -1 mirrors to 0, row 2 to 1.
	wantR := []uint8{10, 0, 0, 10, 20, 30, 30, 20}
	for x, r := range wantR {
		if got := c.RGBAAt(x, 0); got.R != r || got.G != 0 {
			t.Fatalf("column %d row -1: %+v, want R %d G 0", x, got, r)
		}
	}
	if got := c.RGBAAt(2, 3); got.G != 100 {
		t.Fatalf("row 2 should mirror row 1: %+v", got)
	}
}

func TestStitchSingleTileThatExtendsPastTheImage(t *testing.T) {
	const k = 4
	g := truth(1024, 512, 6) // 2:1 fits no ratio exactly
	plan := scaled(g, 256, 128)
	ts, err := Grid(256, 128, 1, 0.2, nb2Ratios)
	if err != nil {
		t.Fatal(err)
	}
	tl := ts[0]
	if !tl.Outside {
		t.Fatalf("tile = %+v, want it to extend past the image", tl)
	}
	// What a model sees and returns: the mirrored crop, re-rendered larger.
	src := Crop(g, Box{X: tl.Box.X * k, Y: tl.Box.Y * k, W: tl.Box.W * k, H: tl.Box.H * k})
	tile := scaled(src, tl.Box.W*k*5/4, tl.Box.H*k*5/4)
	a := Align(plan, tl.Box, tile, Options{})
	if a.Status != Placed || a.Match < 0.9 {
		t.Fatalf("align = %+v", a)
	}
	ps := []Placement{{Box: tl.Box, Align: a}}
	Feathers(ps, 256, 128, 0.2)
	if ps[0].Border != [4]bool{true, true, true, true} {
		t.Fatalf("borders = %v", ps[0].Border)
	}
	c := NewCanvas(plan, 1024, 512)
	c.Reserve(ps)
	c.Paint(c.Prepare(ps[0], tile))
	if got, base := meanAbsDiff(c.Img, g, g.Bounds()), meanAbsDiff(scaled(plan, 1024, 512), g, g.Bounds()); got > base*0.7 {
		t.Fatalf("single-tile error %.2f not clearly below interpolation %.2f", got, base)
	}
}

func TestResizeWindowMatchesResize(t *testing.T) {
	src := truth(97, 61, 3)
	for _, size := range [][2]int{{300, 190}, {40, 25}} {
		w, h := size[0], size[1]
		full := Resize(src, src.Bounds(), w, h)
		win := image.Rect(w/3, h/4, w-5, h/2+3)
		part := ResizeWindow(src, src.Bounds(), w, h, win)
		if part.Rect != win {
			t.Fatalf("%dx%d: window %v, want %v", w, h, part.Rect, win)
		}
		for y := win.Min.Y; y < win.Max.Y; y++ {
			for x := win.Min.X; x < win.Max.X; x++ {
				if part.RGBAAt(x, y) != full.RGBAAt(x, y) {
					t.Fatalf("%dx%d: pixel %d,%d = %v, want %v", w, h, x, y, part.RGBAAt(x, y), full.RGBAAt(x, y))
				}
			}
		}
	}
	if r := ResizeWindow(src, src.Bounds(), 50, 50, image.Rect(60, 0, 70, 10)).Rect; !r.Empty() {
		t.Fatalf("a window outside the result = %v, want empty", r)
	}
}

func TestPrepareResamplesOnlyWhatLandsOnTheCanvas(t *testing.T) {
	// A 32x32 box over a 16x16 image stitched to 512x512: the whole tile
	// resamples to 1024x1024, but only the canvas part is needed.
	g := truth(512, 512, 7)
	plan := scaled(g, 16, 16)
	tile := scaled(Crop(g, Box{X: -256, Y: -256, W: 1024, H: 1024}), 640, 640)
	ps := []Placement{{Box: Box{X: -8, Y: -8, W: 32, H: 32}}}
	Feathers(ps, 16, 16, 0.2)
	c := NewCanvas(plan, 512, 512)
	c.Reserve(ps)
	pp := c.Prepare(ps[0], tile)
	if pp.sw != 1024 || pp.sh != 1024 || pp.src.Rect.Dx() > 515 || pp.src.Rect.Dy() > 515 {
		t.Fatalf("prepared %dx%d of a %dx%d resample, want about the 512x512 canvas", pp.src.Rect.Dx(), pp.src.Rect.Dy(), pp.sw, pp.sh)
	}
	// It paints exactly what the whole resampled tile would.
	whole := &Prepared{p: pp.p, f: pp.f, sw: pp.sw, sh: pp.sh, src: Resize(tile, tile.Bounds(), pp.sw, pp.sh)}
	c2 := NewCanvas(plan, 512, 512)
	c2.Reserve(ps)
	c.Paint(pp)
	c2.Paint(whole)
	if !bytes.Equal(c.Img.Pix, c2.Img.Pix) {
		t.Fatal("the windowed tile paints different pixels")
	}

	for _, tc := range []struct {
		p0, p1 int
		f0     float64
		lo, hi int
	}{
		{0, 10, 500, 0, 1},     // mapped entirely before the tile: its first pixel
		{0, 10, -500, 99, 100}, // entirely after it: its last pixel
		{20, 30, 10, 9, 21},
	} {
		if lo, hi := readSpan(tc.p0, tc.p1, tc.f0, 1, 100); lo != tc.lo || hi != tc.hi {
			t.Errorf("readSpan(%d, %d, %g) = %d, %d; want %d, %d", tc.p0, tc.p1, tc.f0, lo, hi, tc.lo, tc.hi)
		}
	}
}

// A tile cut against the image's edge but aligned inward is not stretched
// out to the edge: the strip it no longer covers keeps the base.
func TestBorderTileMovedInwardKeepsTheBase(t *testing.T) {
	plan := scaled(truth(512, 384, 9), 128, 96)
	box := Box{X: 0, Y: 0, W: 128, H: 96}
	ps := []Placement{{Box: box, Align: Alignment{ScaleX: 1, ScaleY: 1, ShiftX: 6}}}
	Feathers(ps, 128, 96, 0.2)
	if ps[0].Border != [4]bool{false, true, true, true} || ps[0].Feather[0] != 6 {
		t.Fatalf("border %v feather %v, want the left side open with a 6 px feather", ps[0].Border, ps[0].Feather)
	}
	red := image.NewRGBA(image.Rect(0, 0, 512, 384))
	for i := 0; i < len(red.Pix); i += 4 {
		red.Pix[i], red.Pix[i+3] = 255, 255
	}
	base := NewCanvas(plan, 512, 384)
	c := NewCanvas(plan, 512, 384)
	c.Reserve(ps)
	c.Paint(c.Prepare(ps[0], red))
	// 6 plan px is 24 output px: there, nothing but the base.
	if d := meanAbsDiff(c.Img, base.Img, image.Rect(0, 0, 24, 384)); d != 0 {
		t.Fatalf("the uncovered strip differs from the base by %.2f", d)
	}
	if px := c.Img.RGBAAt(300, 200); px != (color.RGBA{255, 0, 0, 255}) {
		t.Fatalf("inside the tile: %v", px)
	}
	// Half a pixel or less still reaches the edge.
	ps[0].Align.ShiftX = 0.4
	Feathers(ps, 128, 96, 0.2)
	if !ps[0].Border[0] {
		t.Fatal("a 0.4 px shift should still cover the edge")
	}
}

func TestPreparedPixelsIsWhatPrepareAllocates(t *testing.T) {
	g := truth(512, 512, 8)
	plan := scaled(g, 16, 16)
	tile := scaled(g, 320, 320)
	ps := []Placement{{Box: Box{X: -8, Y: -8, W: 32, H: 32}}, {Box: Box{X: 4, Y: 4, W: 8, H: 8}}}
	Feathers(ps, 16, 16, 0.2)
	c := NewCanvas(plan, 512, 512)
	for _, p := range ps {
		r := c.Prepare(p, tile).src.Rect
		if got := c.PreparedPixels(p); got != r.Dx()*r.Dy() {
			t.Fatalf("PreparedPixels = %d, Prepare allocated %v", got, r)
		}
	}
}
