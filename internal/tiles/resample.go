package tiles

import (
	"image"
	"math"
	"runtime"
	"sync"

	"golang.org/x/image/draw"
)

// Resize resamples the sr part of src into a new w x h image with a
// separable Catmull-Rom filter, widened when shrinking so that it
// antialiases (as x/image/draw's CatmullRom.Scale does), and spread across
// CPUs.
func Resize(src image.Image, sr image.Rectangle, w, h int) *image.RGBA {
	return ResizeWindow(src, sr, w, h, image.Rect(0, 0, w, h))
}

// ResizeWindow is Resize computing only the pixels of the w x h result
// inside win, with the same values. The returned image's Rect is win
// clipped to the result.
func ResizeWindow(src image.Image, sr image.Rectangle, w, h int, win image.Rectangle) *image.RGBA {
	win = win.Intersect(image.Rect(0, 0, w, h))
	dst := image.NewRGBA(win)
	if win.Empty() || sr.Empty() {
		return dst
	}
	rgba := asRGBA(src, sr)
	sw, sh := rgba.Rect.Dx(), rgba.Rect.Dy()
	if sw == 0 || sh == 0 {
		return dst
	}
	xs := filterTaps(sw, w, win.Min.X, win.Max.X)
	ys := filterTaps(sh, h, win.Min.Y, win.Max.Y)
	w, h = win.Dx(), win.Dy()

	// Bands of output rows; each band filters the source rows it needs
	// horizontally into a scratch buffer, then vertically into dst.
	const band = 64
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for y0 := 0; y0 < h; y0 += band {
		y1 := min(y0+band, h)
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			s0, s1 := ys[y0].first, 0
			for y := y0; y < y1; y++ {
				s0 = min(s0, ys[y].first)
				s1 = max(s1, ys[y].first+len(ys[y].w))
			}
			tmp := make([]float32, (s1-s0)*w*4)
			for sy := s0; sy < s1; sy++ {
				row := rgba.Pix[sy*rgba.Stride:]
				out := tmp[(sy-s0)*w*4:]
				for x, t := range xs {
					var r, g, b, a float32
					for i, wt := range t.w {
						p := row[4*(t.first+i):]
						r += float32(p[0]) * wt
						g += float32(p[1]) * wt
						b += float32(p[2]) * wt
						a += float32(p[3]) * wt
					}
					o := out[4*x:]
					o[0], o[1], o[2], o[3] = r, g, b, a
				}
			}
			for y := y0; y < y1; y++ {
				t := ys[y]
				drow := dst.Pix[y*dst.Stride:]
				for x := range w {
					var r, g, b, a float32
					for i, wt := range t.w {
						p := tmp[((t.first+i-s0)*w+x)*4:]
						r += p[0] * wt
						g += p[1] * wt
						b += p[2] * wt
						a += p[3] * wt
					}
					o := drow[4*x:]
					o[0], o[1], o[2], o[3] = to8(r), to8(g), to8(b), to8(a)
				}
			}
		}()
	}
	wg.Wait()
	return dst
}

// asRGBA returns the sr part of src as an *image.RGBA starting at the
// origin, without copying when src already is one.
func asRGBA(src image.Image, sr image.Rectangle) *image.RGBA {
	if r, ok := src.(*image.RGBA); ok {
		sub := r.SubImage(sr).(*image.RGBA)
		sub.Rect = sub.Rect.Sub(sub.Rect.Min)
		return sub
	}
	dst := image.NewRGBA(image.Rect(0, 0, sr.Dx(), sr.Dy()))
	draw.Draw(dst, dst.Bounds(), src, sr.Min, draw.Src)
	return dst
}

// taps are the source indices and weights of one output sample.
type taps struct {
	first int
	w     []float32
}

// filterTaps computes Catmull-Rom weights mapping n source samples to m,
// for output samples i0 to i1 (exclusive).
func filterTaps(n, m, i0, i1 int) []taps {
	scale := float64(m) / float64(n)
	stretch := math.Max(1, 1/scale) // widen the kernel when shrinking
	support := 2 * stretch
	out := make([]taps, 0, i1-i0)
	for i := i0; i < i1; i++ {
		c := (float64(i)+0.5)/scale - 0.5
		lo := int(math.Ceil(c - support))
		hi := int(math.Floor(c + support))
		ws := make([]float64, 0, hi-lo+1)
		sum := 0.0
		for j := lo; j <= hi; j++ {
			v := catmullRom((float64(j) - c) / stretch)
			ws = append(ws, v)
			sum += v
		}
		// Clamp out-of-range taps to the edge by folding their weight in.
		first := max(lo, 0)
		last := min(hi, n-1)
		w := make([]float32, last-first+1)
		for k, v := range ws {
			j := min(max(lo+k, 0), n-1)
			w[j-first] += float32(v / sum)
		}
		out = append(out, taps{first: first, w: w})
	}
	return out
}

func catmullRom(x float64) float64 {
	x = math.Abs(x)
	switch {
	case x < 1:
		return 1.5*x*x*x - 2.5*x*x + 1
	case x < 2:
		return -0.5*x*x*x + 2.5*x*x - 4*x + 2
	}
	return 0
}

func to8(v float32) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}

// Crop copies box out of img. Parts of box outside the image are filled by
// mirroring the image across its border (see Tile.Outside).
func Crop(img image.Image, b Box) *image.RGBA {
	src := asRGBA(img, img.Bounds())
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, b.W, b.H))
	for y := range b.H {
		srow := src.Pix[mirror(b.Y+y, h)*src.Stride:]
		drow := dst.Pix[y*dst.Stride:]
		if b.X >= 0 && b.X1() <= w {
			copy(drow[:4*b.W], srow[4*b.X:4*b.X1()])
			continue
		}
		for x := range b.W {
			sx := mirror(b.X+x, w)
			copy(drow[4*x:4*x+4], srow[4*sx:4*sx+4])
		}
	}
	return dst
}

// mirror reflects index i into [0, n).
func mirror(i, n int) int {
	if n <= 1 {
		return 0
	}
	p := 2 * n
	i %= p
	if i < 0 {
		i += p
	}
	if i >= n {
		i = p - 1 - i
	}
	return i
}
