package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/draw"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

// scene renders a deterministic textured image with structure at every scale.
func scene(w, h int, seed uint64) *image.RGBA {
	rng := rand.New(rand.NewPCG(seed, 7))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	type blob struct{ x, y, r float64 }
	blobs := make([]blob, 30)
	for i := range blobs {
		blobs[i] = blob{rng.Float64() * float64(w), rng.Float64() * float64(h), 8 + rng.Float64()*float64(w)/10}
	}
	for y := range h {
		for x := range w {
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			v := [3]float64{120 + 60*math.Sin(fx*7+fy*3), 110 + 50*math.Cos(fx*5-fy*6), 100 + 40*math.Sin(fx*11*fy+2)}
			for i, b := range blobs {
				if dx, dy := float64(x)-b.x, float64(y)-b.y; dx*dx+dy*dy < b.r*b.r {
					v[i%3] += 35
				}
			}
			n := (rng.Float64() - 0.5) * 24
			img.SetRGBA(x, y, color.RGBA{c8(v[0] + n), c8(v[1] + n), c8(v[2] + n), 255})
		}
	}
	return img
}

func c8(v float64) uint8 { return uint8(math.Max(0, math.Min(255, math.Round(v)))) }

func resized(src image.Image, r image.Rectangle, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, r, draw.Src, nil)
	return dst
}

func encode(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeEdits renders, for each planned tile, what an ideal model returns: the
// truth at the crop (truth is k times the tiled image), slightly drifted,
// saved in the output directory like an edit_image result.
func fakeEdits(t *testing.T, e *env, truth *image.RGBA, k float64, res *TileImageResult) []StitchTile {
	t.Helper()
	var out []StitchTile
	for i, pt := range res.Tiles {
		b := pt.Box
		dx, dy := float64(i%3-1)*1.5, float64(i%2)*-1.0 // plan pixels
		r := image.Rect(int((float64(b.X)+dx)*k), int((float64(b.Y)+dy)*k), int((float64(b.X1())+dx)*k), int((float64(b.Y1())+dy)*k))
		tw := int(float64(b.W) * k * 1.25)
		th := int(math.Round(float64(tw) * float64(b.H) / float64(b.W)))
		a, err := e.store.Save("image", pt.Crop.Name+"-edit", "png", encode(t, resized(truth, r, tw, th)), "image/png", nil)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, StitchTile{Tile: pt.Tile, Image: a.URI})
	}
	return out
}

func lumaError(a, b *image.RGBA) float64 {
	var sum float64
	n := 0
	for y := 0; y < a.Rect.Dy(); y += 2 {
		for x := 0; x < a.Rect.Dx(); x += 2 {
			pa, pb := a.RGBAAt(x, y), b.RGBAAt(x, y)
			sum += math.Abs(0.299*(float64(pa.R)-float64(pb.R)) + 0.587*(float64(pa.G)-float64(pb.G)) + 0.114*(float64(pa.B)-float64(pb.B)))
			n++
		}
	}
	return sum / float64(n)
}

func decodeFile(t *testing.T, path string) *image.RGBA {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return normalizeRGBA(img).(*image.RGBA)
}

func TestTileAndStitchTwoPasses(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(1024, 768, 1)
	src := resized(truth, truth.Bounds(), 256, 192)
	srcPath := filepath.Join(t.TempDir(), "portrait.png")
	if err := os.WriteFile(srcPath, encode(t, src), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: srcPath, LongEdge: 1024, Grid: 3, ImageSize: "4K"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pass != 1 || res.Mode != "grid" || len(res.Tiles) != 9 || res.Output != (Size{1024, 768}) || res.Image != (Size{256, 192}) {
		t.Fatalf("plan = %+v", res)
	}
	if res.Model != "gemini-nano-banana-2.1" || res.ImageSize != "4K" || res.Cost.USD < 0.9 || res.Cost.USD > 1.5 {
		t.Fatalf("model %s size %s cost %+v", res.Model, res.ImageSize, res.Cost)
	}
	if p, err := e.store.Provenance(res.Tiles[0].Crop.Name); err != nil || p.Model != res.Model {
		t.Fatalf("crop provenance = %+v %v (edit_image should default to the plan's model)", p, err)
	}
	if res.Reference == nil || res.Reference.Width != 256 || !strings.HasPrefix(res.Job, store.URIScheme) || res.NativeLongEdge < 1024 {
		t.Fatalf("reference %+v job %s native %d", res.Reference, res.Job, res.NativeLongEdge)
	}
	if c := res.Tiles[0]; c.Crop.Name != "portrait-p1-t1-r1c1.png" || c.AspectRatio == "" || c.Crop.Width != c.Box.W {
		t.Fatalf("first tile = %+v", c)
	}
	if e.api.ContentCalls != nil {
		t.Fatal("tile_image must not call the model")
	}

	edits := fakeEdits(t, e, truth, 4, res)
	out, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: edits})
	if err != nil {
		t.Fatal(err)
	}
	if out.File.Width != 1024 || out.File.Height != 768 || out.File.Name != "portrait-upscaled.png" || out.Pass != 1 {
		t.Fatalf("stitched = %+v", out.File)
	}
	for _, tr := range out.Tiles {
		if tr.Status != "placed" || tr.Match < 0.9 {
			t.Fatalf("tile report %+v", tr)
		}
	}
	if len(out.Previews) != 5 || len(out.Details) != 4 || !strings.Contains(out.Next, "adds no resolution") {
		t.Fatalf("%d previews, details %v", len(out.Previews), out.Details)
	}
	got := decodeFile(t, out.File.Path)
	interp := resized(src, src.Bounds(), 1024, 768)
	if g, i := lumaError(got, truth), lumaError(interp, truth); g > i*0.7 {
		t.Fatalf("stitched error %.2f not clearly below interpolation %.2f", g, i)
	}
	p, err := e.store.Provenance(out.File.Name)
	if err != nil || p.Tool != "stitch_tiles" || intParam(p.Params, "pass") != 1 || p.Params["reference"] != res.Reference.URI {
		t.Fatalf("provenance = %+v %v", p, err)
	}

	// Refinement pass on the stitched image: pass 2, same size, same reference.
	res2, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: out.File.URI, Regions: []TileRegion{{X: 0.3, Y: 0.3, Width: 0.2, Height: 0.25, Label: "Left Eye"}}, LongEdge: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Pass != 2 || res2.Mode != "regions" || res2.Output != (Size{1024, 768}) || res2.Reference != nil || !strings.Contains(res2.Next, "without referenceImages") {
		t.Fatalf("pass 2 plan = %+v", res2)
	}
	if res2.Tiles[0].Label != "left-eye" || !strings.Contains(strings.Join(res2.Warnings, " "), "longEdge is ignored") || strings.Contains(strings.Join(res2.Warnings, " "), "tighter regions") ||
		!strings.Contains(strings.Join(res2.Warnings, " "), "adds no resolution") || !strings.Contains(res2.Cost.Breakdown, "1 input image") {
		t.Fatalf("pass 2 tile %+v warnings %v", res2.Tiles[0], res2.Warnings)
	}
	out2, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res2.Job, Tiles: fakeEdits(t, e, truth, 1, res2)})
	if err != nil {
		t.Fatal(err)
	}
	if out2.Pass != 2 || out2.File.Name != "portrait-upscaled-p2.png" || out2.Tiles[0].Status != "placed" || len(out2.Details) != 1 || out2.NativeLongEdge != out.NativeLongEdge {
		t.Fatalf("pass 2 = %+v", out2)
	}
}

func TestTileImageValidation(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	src := filepath.Join(t.TempDir(), "s.png")
	if err := os.WriteFile(src, encode(t, scene(400, 300, 2)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, req := range []TileImageRequest{
		{},
		{Image: src, Grid: 5},
		{Image: src, Grid: -1},
		{Image: src, Padding: 0.7},
		{Image: src, LongEdge: 20000},
		{Image: src, LongEdge: 16384}, // 16384 x 12288 is far over the pixel cap
		{Image: src, ImageSize: "8K"},
		{Image: src, Model: "nb2-lite", ImageSize: "4K"},
		{Image: src, Regions: []TileRegion{{X: 0.5, Y: 0.5, Width: 0, Height: 0.1}}},
	} {
		if _, err := e.svc.TileImage(context.Background(), req); apperr.KindOf(err) != apperr.Invalid {
			t.Errorf("%+v: err = %v, want invalid", req, err)
		}
	}
}

func TestTileImageAcceptsDataURIs(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(encode(t, scene(300, 300, 3)))
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: uri, Grid: 2, OutputName: "Shot 1"})
	if err != nil {
		t.Fatal(err)
	}
	_, data, err := e.store.Open(res.Job)
	if err != nil || !strings.Contains(string(data), `"image": "gemini-media://files/shot-1-p1-source.png"`) || strings.Contains(string(data), "base64") {
		t.Fatalf("job = %s (%v)", data, err)
	}
	if res.Output != (Size{8192, 8192}) || len(res.Tiles) != 4 {
		t.Fatalf("plan = %+v", res)
	}
}

func TestStitchTilesErrors(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(1024, 768, 4)
	src := filepath.Join(t.TempDir(), "s.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 256, 192)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	edits := fakeEdits(t, e, truth, 4, res)
	bad := []StitchTilesRequest{
		{Job: ""},
		{Job: res.Job},
		{Job: res.Job, Tiles: []StitchTile{{Tile: 9, Image: edits[0].Image}}},
		{Job: res.Job, Tiles: []StitchTile{edits[0], edits[0]}},
		{Job: res.Job, Tiles: edits, Feather: 0.9},
		{Job: res.Reference.URI, Tiles: edits},
	}
	for _, req := range bad {
		if _, err := e.svc.StitchTiles(context.Background(), req); apperr.KindOf(err) != apperr.Invalid {
			t.Errorf("%+v: err = %v, want invalid", req, err)
		}
	}

	// An unrelated tile is rejected and its area keeps the base.
	other, err := e.store.Save("image", "unrelated", "png", encode(t, scene(480, 360, 99)), "image/png", nil)
	if err != nil {
		t.Fatal(err)
	}
	mixed := append([]StitchTile{{Tile: 1, Image: other.URI}}, edits[1:]...)
	out, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: mixed})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tiles[0].Status != "rejected" || len(out.Warnings) == 0 || !strings.Contains(out.Next, "Retry") {
		t.Fatalf("report = %+v", out)
	}
	// Only unrelated tiles: nothing to stitch.
	if _, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: []StitchTile{{Tile: 1, Image: other.URI}}}); apperr.KindOf(err) != apperr.Invalid {
		t.Fatalf("all rejected: err = %v", err)
	}

	// A tile left out keeps the base: the result claims no model detail
	// figure and asks for the missing tile.
	out, err = e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: edits[1:]})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tiles[0].Status != "unedited" || out.NativeLongEdge != 0 || !strings.Contains(strings.Join(out.Warnings, " "), "were not passed") || !strings.Contains(out.Next, "missing tiles") {
		t.Fatalf("partial stitch = %+v", out)
	}

	// A job file with tampered geometry is refused before anything is allocated.
	_, data, err := e.store.Open(res.Job)
	if err != nil {
		t.Fatal(err)
	}
	var job map[string]any
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(map[string]any){
		"huge output": func(j map[string]any) { j["outWidth"], j["outHeight"] = 1<<40, 1<<40 },
		"wrong shape": func(j map[string]any) { j["outHeight"] = 100 },
		"huge box": func(j map[string]any) {
			j["tiles"].([]any)[0].(map[string]any)["box"] = map[string]any{"x": 0, "y": 0, "width": 1 << 40, "height": 1 << 40}
		},
	} {
		var j map[string]any
		_ = json.Unmarshal(data, &j)
		edit(j)
		raw, _ := json.Marshal(j)
		path := filepath.Join(e.store.Dir(), "tampered.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: path, Tiles: edits}); apperr.KindOf(err) != apperr.Invalid || !strings.Contains(err.Error(), "not a usable") {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// The tiled image changed after tile_image.
	if err := os.WriteFile(src, encode(t, scene(256, 192, 5)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: edits}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed source: err = %v", err)
	}
}

func TestTileImageWarnsAboutLargeRegions(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	// At 1K the model renders about 1250 px per crop, so a region spanning
	// most of a 1200 px image gains almost nothing.
	img := image.NewRGBA(image.Rect(0, 0, 1200, 800))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 7)
	}
	src := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(src, encode(t, img), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, ImageSize: "1K", Regions: []TileRegion{
		{X: 0.05, Y: 0.05, Width: 0.9, Height: 0.9, Label: "everything"},
		{X: 0.4, Y: 0.4, Width: 0.15, Height: 0.15, Label: "detail"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(res.Warnings, " ")
	if !strings.Contains(w, "region 1 (everything)") || strings.Contains(w, "region 2") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
}

func TestTileImagePlansAutomatically(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	for _, c := range []struct {
		w, h, long, grid int
		size             string
	}{
		{256, 256, 1024, 1, "1K"},
		{512, 384, 2048, 1, "2K"},
		{432, 768, 4096, 1, "4K"},
		{1024, 683, 8192, 2, "4K"},  // 2x2 of 4K tiles carries about 8,350 px
		{1024, 683, 10000, 3, "4K"}, // beyond that, 3x3
		{900, 500, 1024, 1, "1K"},   // 1.8:1 fits no ratio: the tile extends past the image
	} {
		img := image.NewRGBA(image.Rect(0, 0, c.w, c.h))
		for i := range img.Pix {
			img.Pix[i] = uint8(i * 13)
		}
		src := filepath.Join(t.TempDir(), "in.png")
		if err := os.WriteFile(src, encode(t, img), 0o600); err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, LongEdge: c.long})
		if err != nil {
			t.Fatal(err)
		}
		if res.Grid != c.grid || res.ImageSize != c.size || len(res.Tiles) != c.grid*c.grid || res.PlanNote == "" || res.NativeLongEdge < c.long*97/100 {
			t.Errorf("%dx%d to %d: grid %d size %s, %d tiles, native %d, note %q", c.w, c.h, c.long, res.Grid, res.ImageSize, len(res.Tiles), res.NativeLongEdge, res.PlanNote)
		}
		if c.w == 900 {
			tl := res.Tiles[0]
			if !tl.Outside || tl.Box.Y >= 0 || tl.Crop.Width != tl.Box.W || tl.Crop.Height != tl.Box.H {
				t.Errorf("900x500 tile = %+v", tl)
			}
		}
	}
}

// A file:// image keeps working after tile_image: the job stores it as given.
func TestTileImageAcceptsFileURIs(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(512, 384, 6)
	src := filepath.Join(t.TempDir(), "uri.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 256, 192)), 0o600); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + filepath.ToSlash(src)
	if !strings.HasPrefix(src, "/") {
		uri = "file:///" + filepath.ToSlash(src) // Windows drive paths
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: uri, Grid: 1, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: fakeEdits(t, e, truth, 2, res)}); err != nil {
		t.Fatalf("stitching a file:// job: %v", err)
	}
}
