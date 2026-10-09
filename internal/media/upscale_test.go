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
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/image/draw"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
	"github.com/mordor-forge/gemini-media-mcp/internal/tiles"
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
		// Past the image's edges the crop is mirrored, as tile_image cuts it.
		crop := tiles.Crop(truth, tiles.Box{X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()})
		a, err := e.store.Save("image", pt.Crop.Name+"-edit", "png", encode(t, resized(crop, crop.Rect, tw, th)), "image/png", nil)
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
	if res.Prompt != tilePrompt || !strings.Contains(res.Next, "prompt = this result's prompt") {
		t.Fatalf("prompt %q next %q", res.Prompt, res.Next)
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
	if res2.Pass != 2 || res2.Mode != "regions" || res2.Output != (Size{1024, 768}) || res2.Reference != nil || !strings.Contains(res2.Next, "without referenceImages") || res2.Prompt != refinePrompt {
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
	if out2.Pass != 2 || out2.File.Name != "portrait-upscaled-p2.png" || out2.Tiles[0].Status != "placed" || len(out2.Details) != 1 ||
		out2.NativeLongEdge > out.NativeLongEdge || out2.NativeLongEdge < out.NativeLongEdge*98/100 { // the region renders at about pass 1's density
		t.Fatalf("pass 2 = %+v (pass 1 native %d)", out2, out.NativeLongEdge)
	}

	// A region rendered coarser than the image lowers its detail figure.
	b := res2.Tiles[0].Box
	coarse, err := e.store.Save("image", "coarse-eye", "png", encode(t, resized(truth, image.Rect(b.X, b.Y, b.X1(), b.Y1()), b.W/2, b.H/2)), "image/png", nil)
	if err != nil {
		t.Fatal(err)
	}
	out3, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res2.Job, Tiles: []StitchTile{{Tile: 1, Image: coarse.URI}}})
	if err != nil || out3.Tiles[0].Status != "placed" {
		t.Fatalf("coarse region = %+v %v", out3, err)
	}
	if out3.NativeLongEdge < 480 || out3.NativeLongEdge > 540 {
		t.Fatalf("coarse region native = %d, want about 512 (half of the 1024 px image)", out3.NativeLongEdge)
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
	// Inline images are capped well below files, over stdio too.
	defer func(v int64) { maxInlineBytes = v }(maxInlineBytes)
	maxInlineBytes = 1 << 10
	_, err = e.svc.TileImage(context.Background(), TileImageRequest{Image: uri, Grid: 2})
	if ae, ok := apperr.As(err); !ok || !strings.Contains(ae.Hint, "inline data: inputs are limited") {
		t.Fatalf("oversized inline image: %v", err)
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
		if i%4 == 3 {
			img.Pix[i] = 255 // opaque
		}
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
			if i%4 == 3 {
				img.Pix[i] = 255 // opaque
			}
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
	// The PNG encode of a large stitch takes seconds too.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	ctx = WithProgress(ctx, func(msg string, _, _ float64) {
		if msg == "encoding PNG" {
			cancel()
		}
	})
	if _, err := e.svc.StitchTiles(ctx, StitchTilesRequest{Job: res.Job, Tiles: edits}); apperr.KindOf(err) != apperr.Canceled {
		t.Fatalf("canceled while encoding: err = %v", err)
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
	if err := os.WriteFile(src, encode(t, image.NewGray(image.Rect(0, 0, 30000, 100))), 0o600); err != nil {
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
	if apperr.KindOf(err) != apperr.Invalid || !strings.Contains(err.Error(), "limit is 24 megapixels") {
		t.Fatalf("err = %v", err)
	}

	// The same header at 16 bits per channel decodes to twice the bytes:
	// 64 MP is under the plan limit at 8 bits, over it at 16.
	ihdr[8] = 16
	chunk = append([]byte("IHDR"), ihdr...)
	png16 := append([]byte("\x89PNG\r\n\x1a\n"), 0, 0, 0, 13)
	png16 = append(png16, chunk...)
	png16 = binary.BigEndian.AppendUint32(png16, crc32.ChecksumIEEE(chunk))
	_, err = decodeImage(&store.Input{Data: png16, MIMEType: "image/png", Ref: "deep.png"}, "image", maxPlanPixels)
	if ae, ok := apperr.As(err); !ok || !strings.Contains(ae.Message, "16 bits per channel") || !strings.Contains(ae.Hint, "8 bits") {
		t.Fatalf("16-bit: err = %v", err)
	}
	if _, err := decodeImage(&store.Input{Data: png, MIMEType: "image/png", Ref: "big.png"}, "image", maxPlanPixels); err == nil || strings.Contains(err.Error(), "megapixels") {
		t.Fatalf("8-bit 64 MP passes the size check (and fails decoding the stub): err = %v", err)
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
	// 8K tiles (67 MP) are more than stitch_tiles decodes.
	if got := editSizes(m); !slices.Equal(got, []string{"1K", "4K"}) {
		t.Fatalf("editSizes = %v", got)
	}
	if got := largestEditSize([]string{"1K", "6K", "2K"}); got != "6K" {
		t.Fatalf("largestEditSize = %s", got)
	}

	// A size the catalog accepts but whose pixels are unknown is refused,
	// and automatic planning leaves it out.
	e := newEnv(t, nil, spend.Budget{})
	override := filepath.Join(t.TempDir(), "override.yaml")
	if err := os.WriteFile(override, []byte("models:\n  - id: gemini-nano-banana-2.1\n    capabilities:\n      imageSizes: [\"1K\", \"2K\", \"4K\", \"8K\", \"HD\"]\n"), 0o600); err != nil {
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
	if _, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: img, Model: "nb2", ImageSize: "8K"}); apperr.KindOf(err) != apperr.Invalid || !strings.Contains(err.Error(), "larger than stitch_tiles accepts") {
		t.Fatalf("8K: err = %v", err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: img, Model: "nb2"})
	if err != nil || res.ImageSize == "8K" || res.ImageSize == "HD" {
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

func TestTileImageValidatesTheInputsEachPassSends(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(512, 384, 14)
	src := filepath.Join(t.TempDir(), "refs.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 128, 96)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: fakeEdits(t, e, truth, 4, res)})
	if err != nil {
		t.Fatal(err)
	}

	// A model that takes a single input image: first-pass tiles go with
	// the reference (two images), refinement crops alone.
	override := filepath.Join(t.TempDir(), "override.yaml")
	if err := os.WriteFile(override, []byte("models:\n  - id: gemini-nano-banana-2.1\n    capabilities:\n      maxReferenceImages: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.NewSource(override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.svc.catalog = cat
	if _, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Model: "nb2"}); apperr.KindOf(err) != apperr.Invalid || !strings.Contains(err.Error(), "at most 1") {
		t.Fatalf("first pass: err = %v", err)
	}
	if _, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: out.File.URI, Model: "nb2", Regions: []TileRegion{{X: 0.3, Y: 0.3, Width: 0.3, Height: 0.3}}}); err != nil {
		t.Fatalf("refinement pass: %v", err)
	}
}

func TestTileImageReferenceFromASeparateOriginal(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	dir := t.TempDir()
	img := filepath.Join(dir, "clean.png")
	if err := os.WriteFile(img, encode(t, scene(320, 640, 15)), 0o600); err != nil {
		t.Fatal(err)
	}
	// The original is a sideways-stored phone JPEG: 64x32 with EXIF 6.
	orig := filepath.Join(dir, "phone.jpg")
	if err := os.WriteFile(orig, exifJPEG(t, 6, binary.LittleEndian), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: img, Original: orig, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Reference; r == nil || r.Width != 32 || r.Height != 64 || r.MIMEType != "image/jpeg" {
		t.Fatalf("reference = %+v, want the original turned upright (32x64)", r)
	}
	if p, err := e.store.Provenance(res.Reference.Name); err != nil || p.Inputs[0] != orig {
		t.Fatalf("reference provenance = %+v %v", p, err)
	}
}

func TestTileImageTrustsProvenanceOnlyForTheStitchedBytes(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(512, 384, 16)
	src := filepath.Join(t.TempDir(), "kept.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 128, 96)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: fakeEdits(t, e, truth, 4, res)})
	if err != nil {
		t.Fatal(err)
	}
	// A refinement pass edits crops alone: an original passed there is ignored.
	refine, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: out.File.URI, Original: src, Regions: []TileRegion{{X: 0.3, Y: 0.3, Width: 0.3, Height: 0.3}}})
	if err != nil || refine.Pass != 2 || refine.Reference != nil || !strings.Contains(strings.Join(refine.Warnings, " "), "original is ignored") {
		t.Fatalf("refinement with original = %+v, %v", refine, err)
	}

	// Edited in place after stitching: its sidecar no longer describes it.
	if err := os.WriteFile(out.File.Path, encode(t, scene(1024, 768, 17)), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: out.File.URI, Grid: 2, LongEdge: 2048})
	if err != nil {
		t.Fatal(err)
	}
	if again.Pass != 1 || again.Reference == nil || again.Output.Width != 2048 || !strings.Contains(strings.Join(again.Warnings, " "), "new first pass") {
		t.Fatalf("changed stitch = pass %d, reference %v, output %v, warnings %v", again.Pass, again.Reference, again.Output, again.Warnings)
	}
}

func TestTileImageStopsBeforeDecodingWhenCanceled(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(encode(t, scene(300, 200, 18)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.svc.TileImage(ctx, TileImageRequest{Image: uri, Grid: 2}); apperr.KindOf(err) != apperr.Canceled {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(e.store.Dir()); len(entries) != 0 {
		t.Fatalf("a canceled tile_image wrote %d files", len(entries))
	}
}

func TestTileImageSaveFailuresSayWhatToFix(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	src := filepath.Join(t.TempDir(), "s.png")
	if err := os.WriteFile(src, encode(t, scene(300, 200, 19)), 0o600); err != nil {
		t.Fatal(err)
	}
	// The output directory is replaced by a file: nothing can be written.
	if err := os.RemoveAll(e.store.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.store.Dir(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2})
	if ae, ok := apperr.As(err); !ok || !strings.Contains(ae.Hint, e.store.Dir()) || !strings.Contains(ae.Hint, "writable") {
		t.Fatalf("err = %v", err)
	}
}

// Tiles too large to hold two at a time are prepared one after the other,
// with the same result.
func TestStitchTilesPreparesSeriallyOverTheBudget(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(512, 384, 20)
	src := filepath.Join(t.TempDir(), "budget.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 128, 96)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	edits := fakeEdits(t, e, truth, 4, res)
	overlapped, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: edits, OutputName: "overlapped"})
	if err != nil {
		t.Fatal(err)
	}
	defer func(v int) { maxPreparedBytes = v }(maxPreparedBytes)
	// Each tile here needs about 2 MB plus the resizer's scratch allowance:
	// 3 MB more than that holds one tile, not two.
	maxPreparedBytes = tiles.MaxResizeScratch + 3<<20
	serial, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: edits, OutputName: "serial"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decodeFile(t, overlapped.File.Path).Pix, decodeFile(t, serial.File.Path).Pix) {
		t.Fatal("preparing tiles one at a time changed the result")
	}
	// A tile that does not fit alone is refused.
	maxPreparedBytes = tiles.MaxResizeScratch
	if _, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: edits}); apperr.KindOf(err) != apperr.Invalid || !strings.Contains(err.Error(), "would need") {
		t.Fatalf("oversized tile: err = %v", err)
	}
}

// Crops stay lossless unless the crop and the reference sent with it would
// not fit one edit request.
func TestEncodeCropFitsTheEditPayload(t *testing.T) {
	img := scene(64, 48, 22)
	if _, ext, mime, err := encodeCrop(context.Background(), img, 1<<20); err != nil || ext != "png" || mime != "image/png" {
		t.Fatalf("small crop: %s %s %v", ext, mime, err)
	}
	data, ext, mime, err := encodeCrop(context.Background(), img, maxEditPayload)
	if err != nil || ext != "jpg" || mime != "image/jpeg" || !bytes.HasPrefix(data, []byte{0xFF, 0xD8}) {
		t.Fatalf("crop over the payload: %s %s %v", ext, mime, err)
	}
}

// The output size tile_image plans for a panorama is rounded; stitch_tiles
// accepts exactly that size and nothing else.
func TestTileJobAcceptsTheRoundedPlannedOutput(t *testing.T) {
	job := tileJob{Version: 1, Width: 6400, Height: 90, OutWidth: 1024, OutHeight: 14, // 14.4 rounded
		Tiles: []jobTile{{Tile: 1, Box: tiles.Box{X: 0, Y: -35, W: 1280, H: 160}}}}
	if err := job.check(); err != nil {
		t.Fatalf("planned output refused: %v", err)
	}
	job.OutHeight = 15
	if err := job.check(); err == nil {
		t.Fatal("an output tile_image never plans should be refused")
	}
}

// A refinement grid with a tile left out keeps the previous image there, so
// its detail figure is the lower of the two rather than none.
func TestPartialRefinementGridKeepsTheDetailFigure(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(1024, 768, 23)
	src := filepath.Join(t.TempDir(), "refine.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 256, 192)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: fakeEdits(t, e, truth, 4, res)})
	if err != nil || out.NativeLongEdge == 0 {
		t.Fatalf("pass 1 = %+v, %v", out, err)
	}
	res2, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: out.File.URI, Grid: 2})
	if err != nil || res2.Pass != 2 || res2.Mode != "grid" {
		t.Fatalf("pass 2 plan = %+v, %v", res2, err)
	}
	out2, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res2.Job, Tiles: fakeEdits(t, e, truth, 1, res2)[1:]})
	if err != nil || out2.NativeLongEdge == 0 || out2.NativeLongEdge > out.NativeLongEdge {
		t.Fatalf("partial refinement native = %d (pass 1 %d), %v", out2.NativeLongEdge, out.NativeLongEdge, err)
	}
}

// A large image is shrunk a band at a time before the reference is cut
// from it; the reference still comes out at the full 2048 px.
func TestTileReferenceShrinksLargeImages(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	big := image.NewGray(image.Rect(0, 0, 5000, 2500)) // not RGBA: no whole-image copy is made
	for i := range big.Pix {
		big.Pix[i] = uint8(i % 251)
	}
	a, err := e.svc.tileReference("orig.png", "big", big)
	if err != nil {
		t.Fatal(err)
	}
	if a.Width != 2048 || a.Height != 1024 || a.MIMEType != "image/jpeg" {
		t.Fatalf("reference = %dx%d %s", a.Width, a.Height, a.MIMEType)
	}
}

// Job files and edited tiles have their own size limits, and an inline job
// is recorded in provenance by type rather than copied.
func TestStitchTilesBoundsJobAndTileFiles(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(512, 384, 24)
	src := filepath.Join(t.TempDir(), "sizes.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 128, 96)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	edits := fakeEdits(t, e, truth, 4, res)
	_, jobData, err := e.store.Open(res.Job)
	if err != nil {
		t.Fatal(err)
	}

	inline := "data:application/json;base64," + base64.StdEncoding.EncodeToString(jobData)
	out, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: inline, Tiles: edits})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := e.store.Provenance(out.File.Name); err != nil || p.Params["job"] != "data:application/json" {
		t.Fatalf("provenance job = %v (%v)", p.Params["job"], err)
	}

	// An inline job over the limit gets the same hint as a file.
	big := "data:application/json;base64," + base64.StdEncoding.EncodeToString(append(jobData, bytes.Repeat([]byte(" "), maxJobBytes)...))
	_, err = e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: big, Tiles: edits})
	if ae, ok := apperr.As(err); !ok || !strings.Contains(ae.Message, "limit") || !strings.Contains(ae.Hint, "job uri") {
		t.Fatalf("oversized inline job: err = %v", err)
	}

	padded := filepath.Join(e.store.Dir(), "padded.json")
	if err := os.WriteFile(padded, append(jobData, bytes.Repeat([]byte(" "), maxJobBytes)...), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: padded, Tiles: edits})
	if ae, ok := apperr.As(err); !ok || !strings.Contains(ae.Message, "limit") || !strings.Contains(ae.Hint, "job uri") {
		t.Fatalf("padded job: err = %v", err)
	}

	// A tile with junk appended past its end is refused before it is read.
	path, tileData, err := e.store.Open(edits[0].Image)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(tileData, make([]byte, maxTileBytes)...), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: edits})
	if ae, ok := apperr.As(err); !ok || !strings.Contains(ae.Message, "tile 1") || !strings.Contains(ae.Hint, "under 64 MB") {
		t.Fatalf("oversized tile file: err = %v", err)
	}

	// The tiled image may be no larger than an image of its size can be.
	f, err := os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, 2<<20)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	_, err = e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: edits})
	if ae, ok := apperr.As(err); !ok || !strings.Contains(ae.Message, "tiled image") || !strings.Contains(ae.Hint, "file tile_image cut") {
		t.Fatalf("padded tiled image: err = %v", err)
	}
}

// When alignment moves an edge tile inward, the strip it no longer covers
// keeps the interpolated image: past 1% of the output, the stitch claims
// no model detail figure, and it always says so.
func TestStitchReportsAnUncoveredEdge(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(1024, 768, 25)
	src := filepath.Join(t.TempDir(), "edge.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 128, 96)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 1, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	// The returned tile shows the crop shifted 5 plan px right.
	b := res.Tiles[0].Box
	const k, shift = 8, 5
	r := image.Rect((b.X+shift)*k, b.Y*k, (b.X1()+shift)*k, b.Y1()*k)
	tile, err := e.store.Save("image", "edge-edit", "png", encode(t, resized(truth, r, b.W*5, b.H*5)), "image/png", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: []StitchTile{{Tile: 1, Image: tile.URI}}})
	if err != nil || out.Tiles[0].Status != "placed" {
		t.Fatalf("stitch = %+v, %v", out, err)
	}
	if out.NativeLongEdge != 0 || !strings.Contains(strings.Join(out.Warnings, " "), "covered by no tile") {
		t.Fatalf("native %d, warnings %v", out.NativeLongEdge, out.Warnings)
	}
}

// One tile_image or stitch_tiles step runs at a time per process; a call
// that cannot get the slot waits, and stops with its request.
func TestUpscaleStepsRunOneAtATime(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	src := filepath.Join(t.TempDir(), "gate.png")
	if err := os.WriteFile(src, encode(t, scene(256, 192, 26)), 0o600); err != nil {
		t.Fatal(err)
	}
	e.svc.imaging <- struct{}{} // another step is running
	waited := false
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ctx = WithProgress(ctx, func(msg string, _, _ float64) {
		if strings.HasPrefix(msg, "waiting for another") {
			waited = true
		}
	})
	if _, err := e.svc.TileImage(ctx, TileImageRequest{Image: src, Grid: 2}); apperr.KindOf(err) != apperr.Timeout || !waited {
		t.Fatalf("while busy: err = %v, waited %v", err, waited)
	}
	<-e.svc.imaging
	if _, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2}); err != nil {
		t.Fatalf("once free: %v", err)
	}
	if len(e.svc.imaging) != 0 {
		t.Fatal("the slot must be released after the call")
	}
}

// At most maxImagingWaiters calls wait for the imaging slot; more are
// refused at once rather than queued with their requests.
func TestImagingWaitersAreBounded(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	e.svc.imaging <- struct{}{} // a step is running
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, maxImagingWaiters)
	for range maxImagingWaiters {
		go func() {
			_, err := e.svc.admitImaging(ctx, "tile_image", "")
			errs <- err
		}()
	}
	for deadline := time.Now().Add(5 * time.Second); e.svc.imagingWaiting.Load() < maxImagingWaiters; {
		if time.Now().After(deadline) {
			t.Fatal("waiters never queued")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := e.svc.admitImaging(context.Background(), "stitch_tiles", ""); apperr.KindOf(err) != apperr.Unavailable || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("over the bound: err = %v", err)
	}
	cancel()
	for range maxImagingWaiters {
		if err := <-errs; apperr.KindOf(err) != apperr.Canceled {
			t.Fatalf("canceled waiter: err = %v", err)
		}
	}
	if n := e.svc.imagingWaiting.Load(); n != 0 {
		t.Fatalf("%d waiters left counted", n)
	}
}

// A transparent image is refused: the model returns opaque tiles.
func TestTileImageRefusesTransparentImages(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	img := image.NewNRGBA(image.Rect(0, 0, 200, 150))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	img.SetNRGBA(10, 10, color.NRGBA{0, 0, 0, 0})
	src := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(src, encode(t, img), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 1})
	if ae, ok := apperr.As(err); !ok || !strings.Contains(ae.Message, "transparent") || !strings.Contains(ae.Hint, "Flatten") {
		t.Fatalf("err = %v", err)
	}
}

// A refinement pass without a model keeps the one pass 1 used.
func TestRefinementPassKeepsTheModel(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(512, 384, 31)
	src := filepath.Join(t.TempDir(), "keep.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 128, 96)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2, LongEdge: 1024, Model: "pro"})
	if err != nil || res.Model != "gemini-3-pro-image" {
		t.Fatalf("pass 1 = %+v %v", res, err)
	}
	out, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: fakeEdits(t, e, truth, 4, res)})
	if err != nil {
		t.Fatal(err)
	}
	region := []TileRegion{{X: 0.3, Y: 0.3, Width: 0.2, Height: 0.25}}
	res2, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: out.File.URI, Regions: region})
	if err != nil || res2.Pass != 2 || res2.Model != "gemini-3-pro-image" {
		t.Fatalf("pass 2 without a model = %+v %v", res2, err)
	}
	if res3, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: out.File.URI, Regions: region, Model: "nb2"}); err != nil || res3.Model != "gemini-nano-banana-2.1" {
		t.Fatalf("pass 2 naming a model = %+v %v", res3, err)
	}
	// A stitched file changed since is a new first pass, on the default.
	changed := decodeFile(t, out.File.Path)
	changed.Pix[0] ^= 0xff
	if err := os.WriteFile(out.File.Path, encode(t, changed), 0o600); err != nil {
		t.Fatal(err)
	}
	res4, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: out.File.URI, Grid: 2, LongEdge: 1024})
	if err != nil || res4.Pass != 1 || res4.Model != "gemini-nano-banana-2.1" || !strings.Contains(strings.Join(res4.Warnings, " "), "changed since stitch_tiles wrote it") {
		t.Fatalf("changed stitch = %+v %v", res4, err)
	}
}

// lateCancel reports cancellation from its second Err call after it is
// armed, so a test can cancel between two checks of one step.
type lateCancel struct {
	context.Context
	armed atomic.Bool
	calls atomic.Int32
}

func (c *lateCancel) Err() error {
	if c.armed.Load() && c.calls.Add(1) > 1 {
		return context.Canceled
	}
	return c.Context.Err()
}

// A cancel that arrives while a crop is being encoded is reported like the
// others, with its retry hint.
func TestTileImageStopsWhileEncodingACrop(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	src := filepath.Join(t.TempDir(), "encode.png")
	if err := os.WriteFile(src, encode(t, scene(256, 192, 41)), 0o600); err != nil {
		t.Fatal(err)
	}
	lc := &lateCancel{Context: context.Background()}
	// The step's own check passes; the encoder's next write sees the cancel.
	ctx := WithProgress(lc, func(msg string, _, _ float64) {
		if strings.HasPrefix(msg, "cutting ") {
			lc.armed.Store(true)
		}
	})
	_, err := e.svc.TileImage(ctx, TileImageRequest{Image: src, Grid: 2, LongEdge: 1024})
	if ae, ok := apperr.As(err); !ok || ae.Kind != apperr.Canceled || !strings.Contains(ae.Hint, "tile_image") {
		t.Fatalf("canceled while encoding: err = %v", err)
	}
}

// stitch_tiles accepts every crop tile_image plans, panoramas included, and
// automatic planning skips plans whose crops tile_image could not cut.
func TestPanoramicPlansStayWithinTheLimits(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	m, _ := catalog.Default().Lookup("nb2")
	ratios := tiles.ParseRatios(m.Capabilities.AspectRatios)

	planned, err := tiles.Grid(10000, 100, 1, 0.2, ratios)
	if err != nil {
		t.Fatal(err)
	}
	outW, outH := fitLong(10000, 100, 8192)
	job := tileJob{Version: 1, Width: 10000, Height: 100, OutWidth: outW, OutHeight: outH,
		Tiles: []jobTile{{Tile: 1, Box: planned[0].Box}}}
	if err := checkPlan(planned, 10000, 100, outW, outH, m.ID); err != nil {
		t.Fatalf("tile_image would refuse the plan: %v", err)
	}
	if err := job.check(); err != nil {
		t.Fatalf("stitch_tiles refuses a crop tile_image cuts (%dx%d): %v", planned[0].Box.W, planned[0].Box.H, err)
	}

	outW, outH = fitLong(40000, 200, 8192)
	p, err := e.svc.autoPlan(m, "", 40000, 200, outW, outH, 0.2, ratios, []int{1, 2, 3, 4}, editSizes(m))
	if err != nil {
		t.Fatal(err)
	}
	if p.grid == 1 || checkPlan(p.tiles, 40000, 200, outW, outH, m.ID) != nil {
		t.Fatalf("automatic plan = grid %d, which tile_image cannot cut", p.grid)
	}
}

// A 16-bit tile holds 8 bytes a pixel decoded plus its RGBA copy, and is
// budgeted that way; such tiles still stitch.
func TestStitchTilesBudgetsSixteenBitTiles(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	truth := scene(512, 384, 42)
	src := filepath.Join(t.TempDir(), "deep.png")
	if err := os.WriteFile(src, encode(t, resized(truth, truth.Bounds(), 128, 96)), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TileImage(context.Background(), TileImageRequest{Image: src, Grid: 2, LongEdge: 1024})
	if err != nil {
		t.Fatal(err)
	}
	edits := fakeEdits(t, e, truth, 4, res)
	for _, ed := range edits {
		path, data, err := e.store.Open(ed.Image)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		deep := image.NewNRGBA64(img.Bounds())
		draw.Draw(deep, deep.Rect, img, img.Bounds().Min, draw.Src)
		var buf bytes.Buffer
		if err := png.Encode(&buf, deep); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tile, _, err := e.svc.loadTile(edits[0].Image, 1)
	if err != nil {
		t.Fatal(err)
	}
	b := tile.Bounds()
	if bpp := decodedBytesPerPixel(tile.ColorModel()); bpp != 8 || prepareBytes(bpp, b.Dx(), b.Dy(), 0)-prepareBytes(4, b.Dx(), b.Dy(), 0) != 4*b.Dx()*b.Dy() {
		t.Fatalf("a 16-bit tile decodes at %d bytes a pixel", bpp)
	}
	if _, err := e.svc.StitchTiles(context.Background(), StitchTilesRequest{Job: res.Job, Tiles: edits}); err != nil {
		t.Fatalf("16-bit tiles: %v", err)
	}
}

// The tile model's schema names no model: the default is whatever the
// server is configured with.
func TestTileModelSchemaNamesNoModel(t *testing.T) {
	f, _ := reflect.TypeOf(TileImageRequest{}).FieldByName("Model")
	if desc := f.Tag.Get("jsonschema"); strings.Contains(desc, "nb2") || !strings.Contains(desc, "default image model") {
		t.Fatalf("model description = %q", desc)
	}
}

// Without prices every plan would cost $0; automatic planning then picks
// the plan with the fewest output pixels that covers the target, not the
// most tiles.
func TestAutoPlanRanksUnpricedModelsByPixels(t *testing.T) {
	e := newEnv(t, nil, spend.Budget{})
	override := filepath.Join(t.TempDir(), "override.yaml")
	if err := os.WriteFile(override, []byte("models:\n  - id: x-image\n    family: gemini-image\n    mediaType: image\n    capabilities:\n      edit: true\n      imageSizes: [\"1K\", \"2K\", \"4K\"]\n      aspectRatios: [\"1:1\", \"4:3\", \"3:4\", \"3:2\", \"2:3\", \"16:9\", \"9:16\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := catalog.NewSource(override, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Status(); err != nil {
		t.Fatal(err)
	}
	m, _ := src.Get().Lookup("x-image")
	ratios := tiles.ParseRatios(m.Capabilities.AspectRatios)
	grids, sizes := []int{1, 2, 3, 4}, editSizes(m)
	outW, outH := fitLong(400, 300, 8192)
	p, err := e.svc.autoPlan(m, "", 400, 300, outW, outH, 0.2, ratios, grids, sizes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.note, "no price for x-image") || float64(p.native) < 0.97*8192 {
		t.Fatalf("plan = grid %d at %s, native %d: %s", p.grid, p.size, p.native, p.note)
	}
	chosen := len(p.tiles) * imageSizePixels(p.size)
	for _, g := range grids {
		for _, size := range sizes {
			planned, err := tiles.Grid(400, 300, g, 0.2, ratios)
			if err != nil || float64(nativeLongEdge(planned, size, 400, 300)) < 0.97*8192 {
				continue
			}
			if px := len(planned) * imageSizePixels(size); px < chosen {
				t.Fatalf("grid %d at %s (%d px) covers the target with fewer pixels than the chosen grid %d at %s (%d px)", g, size, px, p.grid, p.size, chosen)
			}
		}
	}
}
