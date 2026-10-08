package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/image/draw"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
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

	// A tile passed inline is recorded in provenance by type, not copied.
	_, tileData, err := e.store.Open(edits[0].Image)
	if err != nil {
		t.Fatal(err)
	}
	inline := append([]StitchTile{{Tile: edits[0].Tile, Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(tileData)}}, edits[1:]...)
	out, err = e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: inline, OutputName: "inline"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := e.store.Provenance(out.File.Name); err != nil || !slices.Contains(p.Inputs, "data:image/png") || strings.Contains(strings.Join(p.Inputs, " "), "base64") {
		t.Fatalf("provenance inputs = %v (%v)", p.Inputs, err)
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
		"overlapping full tiles": func(j map[string]any) {
			// 16 tiles each covering the whole 48 MP output: 768 MP to blend.
			j["outWidth"], j["outHeight"] = 8000, 6000
			var ts []any
			for i := range 16 {
				ts = append(ts, map[string]any{"tile": i + 1, "label": "all", "box": map[string]any{"x": 0, "y": 0, "width": 256, "height": 192}})
			}
			j["tiles"] = ts
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
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: uri, Grid: 2, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: fakeEdits(t, e, truth, 2, res)})
	if err != nil {
		t.Fatalf("stitching a file:// job: %v", err)
	}
	// The stitched image passed back as a file:// URI is still a refinement pass.
	fileURI := func(p string) string {
		if strings.HasPrefix(p, "/") {
			return "file://" + filepath.ToSlash(p)
		}
		return "file:///" + filepath.ToSlash(p)
	}
	res2, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: fileURI(out.File.Path), Regions: []TileRegion{{X: 0.3, Y: 0.3, Width: 0.3, Height: 0.3, Label: "face"}}})
	if err != nil || res2.Pass != 2 || res2.Reference != nil {
		t.Fatalf("refinement from a file:// URI = %+v %v", res2, err)
	}
}

// A canceled request stops the stitch before anything is saved, whether it
// ends before the call or while tiles are being blended.
func TestStitchTilesStopsWhenCanceled(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(512, 384, 8)
	src := filepath.Join(t.TempDir(), "cancel.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 128, 96)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	edits := fakeEdits(t, e, truth, 4, res)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.svc.StitchTiles(ctx, StitchTilesRequest{Job: res.Job, Tiles: edits}); apperr.KindOf(err) != apperr.Canceled {
		t.Fatalf("canceled before the call: err = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	ctx = WithProgress(ctx, func(msg string, _, _ float64) {
		if strings.HasPrefix(msg, "blended 1/") {
			cancel()
		}
	})
	if _, err := e.svc.StitchTiles(ctx, StitchTilesRequest{Job: res.Job, Tiles: edits}); apperr.KindOf(err) != apperr.Canceled {
		t.Fatalf("canceled while blending: err = %v", err)
	}
	if _, _, err := e.store.Open("cancel-upscaled.png"); err == nil {
		t.Fatal("a canceled stitch must not save its output")
	}
}

// An image far more elongated than the model's widest ratio would need a
// crop many times its size, so planning refuses it.
func TestTileImageRefusesExtremePanoramas(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	src := filepath.Join(t.TempDir(), "strip.png")
	if err := os.WriteFile(src, encode(t, image.NewRGBA(image.Rect(0, 0, 30000, 100))), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 1, ImageSize: "1K"})
	if apperr.KindOf(err) != apperr.Invalid || !strings.Contains(err.Error(), "too elongated") {
		t.Fatalf("err = %v", err)
	}
}

// Tiles are checked against the tile limit from their header, before any
// pixels are allocated.
func TestDecodeImageChecksTheLimitFromTheHeader(t *testing.T) {
	// A PNG that is only a signature and an IHDR chunk claiming 8000x8000.
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 8000)
	binary.BigEndian.PutUint32(ihdr[4:], 8000)
	ihdr[8], ihdr[9] = 8, 6 // 8-bit RGBA
	chunk := append([]byte("IHDR"), ihdr...)
	png := append([]byte("\x89PNG\r\n\x1a\n"), 0, 0, 0, 13)
	png = append(png, chunk...)
	png = binary.BigEndian.AppendUint32(png, crc32.ChecksumIEEE(chunk))
	_, err := decodeImage(&store.Input{Data: png, MIMEType: "image/png", Ref: "big.png"}, "tile 1", maxTilePixels)
	if apperr.KindOf(err) != apperr.Invalid || !strings.Contains(err.Error(), "limit is 40 megapixels") {
		t.Fatalf("err = %v", err)
	}
}

// tile_image stops between crops when the request ends, without writing
// the job.
func TestTileImageStopsWhenCanceled(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	src := filepath.Join(t.TempDir(), "cut.png")
	if err := os.WriteFile(src, encode(t, scene(256, 192, 10)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithProgress(ctx, func(msg string, _, _ float64) {
		if strings.HasPrefix(msg, "1/") {
			cancel()
		}
	})
	if _, err := e.svc.TileImage(ctx, TileImageRequest{Image: src, Grid: 2, LongEdge: 1024}); apperr.KindOf(err) != apperr.Canceled {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := e.store.Open("cut-p1-tiles.json"); err == nil {
		t.Fatal("a canceled tile_image must not write its job")
	}
}

// The model-detail figure follows the sparser axis of each placed tile.
func TestNativeDetailUsesBothAxes(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(512, 384, 11)
	src := filepath.Join(t.TempDir(), "axes.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 128, 96)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 1, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	// The tile comes back 5x as wide as the crop but only 4.75x as tall.
	b := res.Tiles[0].Box
	r := image.Rect(b.X*4, b.Y*4, b.X1()*4, b.Y1()*4)
	a, err := e.store.Save("image", "axes-edit", "png", encode(t, resized(truth, r, b.W*5, b.H*475/100)), "image/png", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: []StitchTile{{Tile: 1, Image: a.URI}}})
	if err != nil || out.Tiles[0].Status != "placed" {
		t.Fatalf("stitch = %+v %v", out, err)
	}
	if want := 4.75 * 128; float64(out.NativeLongEdge) > want*1.02 {
		t.Fatalf("NativeLongEdge = %d, want about %.0f (the vertical density)", out.NativeLongEdge, want)
	}
}

func TestImageSizesComeFromTheCatalog(t *testing.T) {
	for size, want := range map[string]int{"512": 512 * 512, "1K": 1 << 20, "2K": 2048 * 2048, "4K": 4096 * 4096, "8K": 8192 * 8192, "HD": 0, "": 0, "0K": 0, "-1": 0, "99K": 0} {
		if got := imageSizePixels(size); got != want {
			t.Errorf("imageSizePixels(%q) = %d, want %d", size, got, want)
		}
	}
	m := &catalog.Model{Capabilities: catalog.Capabilities{ImageSizes: []string{"512", "1K", "8K", "4K", "HD"}}}
	if got := editSizes(m); !slices.Equal(got, []string{"1K", "8K", "4K"}) {
		t.Fatalf("editSizes = %v", got)
	}
	if got := largestEditSize(editSizes(m)); got != "8K" {
		t.Fatalf("largestEditSize = %s", got)
	}

	// A size the catalog accepts but whose pixels are unknown is refused,
	// and automatic planning leaves it out.
	e := newEnv(t, nil, spend.Budget{})
	override := filepath.Join(t.TempDir(), "override.yaml")
	if err := os.WriteFile(override, []byte("models:\n  - id: gemini-nano-banana-2.1\n    capabilities:\n      imageSizes: [\"1K\", \"2K\", \"4K\", \"HD\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := catalog.NewSource(override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, lastErr := src.Status(); lastErr != nil {
		t.Fatalf("override: %v", lastErr)
	}
	e.svc.catalog = src
	img := filepath.Join(t.TempDir(), "s.png")
	if err := os.WriteFile(img, encode(t, scene(400, 300, 2)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: img, Model: "nb2", ImageSize: "HD"}); apperr.KindOf(err) != apperr.Invalid || !strings.Contains(err.Error(), "omit imageSize") {
		t.Fatalf("unknown size: err = %v", err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: img, Model: "nb2"})
	if err != nil || res.ImageSize == "HD" {
		t.Fatalf("auto plan = %+v, %v", res, err)
	}
}

func TestStitchTilesRefusesATileChangedMidway(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(512, 384, 12)
	src := filepath.Join(t.TempDir(), "swap.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 128, 96)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	edits := fakeEdits(t, e, truth, 4, res)
	path, data, err := e.store.Open(edits[0].Image)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	// Once every tile is aligned, another process replaces tile 1.
	ctx := WithProgress(context.Background(), func(msg string, _, _ float64) {
		if msg == fmt.Sprintf("aligned %d/%d tiles", len(edits), len(edits)) {
			if err := os.WriteFile(path, encode(t, scene(cfg.Width, cfg.Height, 77)), 0o600); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := e.svc.StitchTiles(ctx, StitchTilesRequest{Job: res.Job, Tiles: edits}); apperr.KindOf(err) != apperr.Invalid || !strings.Contains(err.Error(), "changed while stitch_tiles") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := e.store.Open("swap-upscaled.png"); err == nil {
		t.Fatal("a stitch with a changed tile must not save its output")
	}
}

func TestStitchTilesRequiresProvenance(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(512, 384, 13)
	src := filepath.Join(t.TempDir(), "meta.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 128, 96)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	edits := fakeEdits(t, e, truth, 4, res)
	// Without its provenance tile_image would take the result for a new
	// first pass, so the stitch fails rather than report it.
	meta := filepath.Join(e.store.Dir(), ".meta")
	if err := os.RemoveAll(meta); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meta, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: edits})
	if ae, ok := apperr.As(err); !ok || !strings.Contains(ae.Hint, ".meta") || !strings.Contains(ae.Message, "provenance") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := e.store.Open("meta-upscaled.png"); err == nil {
		t.Fatal("a stitched image without provenance must not be left behind")
	}
}
