package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
	"github.com/mordor-forge/gemini-media-mcp/internal/tiles"
)

// Tiled upscale limits.
const (
	defaultLongEdge = 8192
	minLongEdge     = 1024
	maxLongEdge     = 16384
	// maxCanvasPixels bounds the stitched image (about 8 bytes per pixel
	// are held while stitching).
	maxCanvasPixels = 70_000_000
	// maxPlanPixels bounds images tile_image and stitch_tiles decode.
	maxPlanPixels = 100_000_000
	// maxTilePixels bounds an edited tile: the model returns at most about
	// 17 megapixels (4K). With its converted copy and a resampled window
	// no larger than the canvas, one tile then fits maxPreparedBytes.
	maxTilePixels = 24_000_000
	// maxFootprintPixels bounds the output pixels all tiles of a job cover
	// together (what blending visits). Real plans stay at a few times the
	// output; a crafted job of many overlapping full-size tiles does not.
	maxFootprintPixels = 6 * maxCanvasPixels
	// maxCropPixels is the long side of crops and of the reference sent to
	// the model; the model samples inputs at about 1K, so larger files only
	// cost upload time and request size.
	maxCropPixels = 2048
	// localInputBytes bounds files read for local processing (not sent to
	// Google), such as a stitched 8K PNG.
	localInputBytes = 512 << 20
	alignWorkers    = 2
	// minRegionGain is the least output-to-image pixel ratio at which a
	// refinement region adds visible detail.
	minRegionGain = 2.0
	maxGrid       = 4
	maxRegions    = 12
	maxDetails    = 6
	detailPixels  = 512
)

// maxPreparedBytes bounds the memory of the tiles in flight while
// stitching, on top of the canvas: one tile painted and the next prepared
// when both fit, otherwise one at a time; a tile that does not fit alone
// is refused. A variable for tests.
var maxPreparedBytes = 512 << 20

// TileRegion is an area of the image as fractions of its size.
type TileRegion struct {
	X      float64 `json:"x" jsonschema:"Left edge as a fraction of the image width (0-1)."`
	Y      float64 `json:"y" jsonschema:"Top edge as a fraction of the image height (0-1)."`
	Width  float64 `json:"width" jsonschema:"Width as a fraction of the image width."`
	Height float64 `json:"height" jsonschema:"Height as a fraction of the image height."`
	Label  string  `json:"label,omitempty" jsonschema:"Short name used in file names and reports, e.g. face, left-hand, label."`
}

// TileImageRequest is the input of tile_image.
type TileImageRequest struct {
	Image      string       `json:"image" jsonschema:"The image to cut: the original photo for the first pass, or the previous stitch_tiles result for a refinement pass. File path, gemini-media:// URI or data: URI."`
	Grid       int          `json:"grid,omitempty" jsonschema:"Cut a grid x grid layout covering the whole image (1-4). Omit it on a first pass to let tile_image pick the cheapest grid that covers longEdge (explained in planNote); a later full-grid pass defaults to 3. Ignored when regions are given."`
	Regions    []TileRegion `json:"regions,omitempty" jsonschema:"Refinement pass: up to 12 areas to re-render (faces, hands, text, materials), as fractions of the image. Omit for a full grid."`
	Padding    float64      `json:"padding,omitempty" jsonschema:"Context added around each cell or region, as a fraction of its size (default 0.2, 0.05-0.5). Tiles then grow to the nearest aspect ratio the model supports."`
	LongEdge   int          `json:"longEdge,omitempty" jsonschema:"Long edge in pixels of the image stitch_tiles will produce (default 8192, 1024-16384, at most 70 megapixels). First pass only; later passes keep their input's size."`
	Original   string       `json:"original,omitempty" jsonschema:"First pass only: the untouched original, sent with every tile as context (as a copy of at most 2048 px). Defaults to image. Refinement passes edit crops alone and keep the first pass's original, so it is ignored there."`
	Model      string       `json:"model,omitempty" jsonschema:"Image model the tiles will be edited with (default nb2). Sets the supported aspect ratios and the cost estimate."`
	ImageSize  string       `json:"imageSize,omitempty" jsonschema:"Size the tiles will be edited at (1K, 2K or 4K, as the model supports). Omit it on a first pass to pick it with the grid; otherwise it defaults to the model's largest size."`
	OutputName string       `json:"outputName,omitempty" jsonschema:"Optional base name for the crops and the stitched result. Defaults to the original's name."`
}

// PlannedTile is one crop to edit.
type PlannedTile struct {
	Tile        int         `json:"tile"`
	Label       string      `json:"label"`
	Box         tiles.Box   `json:"box" jsonschema:"Crop in pixels of the tiled image"`
	AspectRatio string      `json:"aspectRatio" jsonschema:"Pass it as edit_image aspectRatio so the tile comes back with the crop's shape"`
	Outside     bool        `json:"outside,omitempty" jsonschema:"The crop extends past the image to reach a supported shape; that part is mirrored and discarded when stitching"`
	Crop        store.Asset `json:"crop" jsonschema:"Pass its uri as edit_image image"`
}

// Size is a width and height in pixels.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// TileImageResult is the output of tile_image.
type TileImageResult struct {
	Job            string        `json:"job" jsonschema:"Pass it to stitch_tiles"`
	Pass           int           `json:"pass"`
	Mode           string        `json:"mode" jsonschema:"grid or regions"`
	Grid           int           `json:"grid,omitempty" jsonschema:"Grid size used (grid mode): 1 is the whole image as one tile"`
	PlanNote       string        `json:"planNote,omitempty" jsonschema:"Why this grid and imageSize were chosen, when picked automatically"`
	Image          Size          `json:"image" jsonschema:"Size of the tiled image"`
	Output         Size          `json:"output" jsonschema:"Size stitch_tiles will produce"`
	NativeLongEdge int           `json:"nativeLongEdge,omitempty" jsonschema:"About how many pixels of model-rendered detail the output's long edge carries if every tile comes back at imageSize; beyond that the output is interpolated"`
	Reference      *store.Asset  `json:"reference,omitempty" jsonschema:"First pass only: a copy of the original (at most 2048 px) to pass as edit_image referenceImages for every tile. Refinement passes edit each crop alone."`
	Model          string        `json:"model"`
	ImageSize      string        `json:"imageSize"`
	Tiles          []PlannedTile `json:"tiles"`
	Cost           Cost          `json:"cost" jsonschema:"Estimated cost of editing every tile once (tile_image itself is free)"`
	Next           string        `json:"next"`
	Warnings       []string      `json:"warnings,omitempty"`
}

// tileJob is the manifest tile_image writes and stitch_tiles reads.
type tileJob struct {
	Version     int       `json:"version"`
	Pass        int       `json:"pass"`
	Mode        string    `json:"mode"`
	Name        string    `json:"name"`
	Image       string    `json:"image"`
	ImageSHA256 string    `json:"imageSha256"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	OutWidth    int       `json:"outWidth"`
	OutHeight   int       `json:"outHeight"`
	Original    string    `json:"original"`
	Reference   string    `json:"reference"`
	Model       string    `json:"model"`
	ImageSize   string    `json:"imageSize"`
	PrevNative  int       `json:"prevNativeLongEdge,omitempty"`
	Tiles       []jobTile `json:"tiles"`
	CreatedAt   time.Time `json:"createdAt"`
}

// check bounds a job's geometry before stitch_tiles allocates for it: the
// job is a file a caller could have edited.
func (j *tileJob) check() error {
	const side = 1 << 20 // beyond any real image; keeps the products below from overflowing
	switch {
	case j.Width < tiles.MinTilePixels || j.Height < tiles.MinTilePixels || j.Width > side || j.Height > side || j.Width*j.Height > maxPlanPixels:
		return fmt.Errorf("image size %dx%d is out of range", j.Width, j.Height)
	case j.OutWidth < 1 || j.OutHeight < 1 || j.OutWidth > maxLongEdge || j.OutHeight > maxLongEdge || j.OutWidth*j.OutHeight > maxCanvasPixels:
		return fmt.Errorf("output size %dx%d is out of range (at most %d megapixels)", j.OutWidth, j.OutHeight, maxCanvasPixels/1_000_000)
	case math.Abs(float64(j.OutWidth*j.Height)/float64(j.OutHeight*j.Width)-1) > 0.02:
		return fmt.Errorf("output size %dx%d does not have the shape of the %dx%d image", j.OutWidth, j.OutHeight, j.Width, j.Height)
	case len(j.Tiles) > max(maxGrid*maxGrid, maxRegions):
		return fmt.Errorf("%d tiles is more than tile_image plans", len(j.Tiles))
	}
	boxes := make([]tiles.Box, len(j.Tiles))
	for i, t := range j.Tiles {
		b := t.Box
		if b.W < tiles.MinTilePixels || b.H < tiles.MinTilePixels || b.W > 8*j.Width || b.H > 8*j.Height || b.W*b.H > min(4*j.Width*j.Height, maxPlanPixels) ||
			b.X >= j.Width || b.Y >= j.Height || b.X1() <= 0 || b.Y1() <= 0 {
			return fmt.Errorf("tile %d's box %+v does not fit the %dx%d image", t.Tile, b, j.Width, j.Height)
		}
		boxes[i] = b
	}
	if px := footprintPixels(boxes, j.Width, j.Height, j.OutWidth, j.OutHeight); px > maxFootprintPixels {
		return fmt.Errorf("its tiles together cover %.0f megapixels of output (the limit is %d)", px/1e6, maxFootprintPixels/1_000_000)
	}
	return nil
}

// footprintPixels is how many output pixels the boxes (in w x h image
// pixels) cover together, overlaps counted once per box.
func footprintPixels(boxes []tiles.Box, w, h, outW, outH int) float64 {
	scale := float64(outW) / float64(w) * float64(outH) / float64(h)
	sum := 0.0
	for _, b := range boxes {
		sum += float64(b.W) * float64(b.H) * scale
	}
	return sum
}

type jobTile struct {
	Tile        int       `json:"tile"`
	Label       string    `json:"label"`
	Box         tiles.Box `json:"box"`
	Core        tiles.Box `json:"core"`
	AspectRatio string    `json:"aspectRatio"`
	Crop        string    `json:"crop"`
}

// TileImage cuts an image into overlapping crops for a tiled upscale. It
// makes no model calls.
func (s *Service) TileImage(ctx context.Context, req TileImageRequest) (*TileImageResult, error) {
	if strings.TrimSpace(req.Image) == "" {
		return nil, apperr.Invalidf("image is required")
	}
	padding := req.Padding
	if padding == 0 {
		padding = 0.2
	}
	if padding < 0.05 || padding > 0.5 {
		return nil, apperr.Invalidf("padding must be between 0.05 and 0.5 (got %g)", req.Padding)
	}
	if req.Grid < 0 || req.Grid > maxGrid {
		return nil, apperr.Invalidf("grid must be between 1 and %d (got %d), or omitted to plan automatically", maxGrid, req.Grid)
	}
	if len(req.Regions) > maxRegions {
		return nil, apperr.Invalidf("at most %d regions per pass (got %d); split them over two passes", maxRegions, len(req.Regions))
	}

	r, location, warnings, err := s.resolve(req.Model, catalog.Image)
	if err != nil {
		return nil, err
	}
	m := r.Model
	if !m.Capabilities.Edit {
		return nil, apperr.Invalidf("%s cannot edit images; use nb2 or pro", m.ID)
	}
	sizes := editSizes(m)
	if len(sizes) == 0 {
		return nil, apperr.Invalidf("the catalog lists no output sizes for %s that tile_image can plan with (512, 1K, 2K, 4K, ...); pick nb2 or pro", m.ID)
	}
	size := strings.ToUpper(strings.TrimSpace(req.ImageSize))
	if size != "" && imageSizePixels(size) == 0 {
		return nil, apperr.Invalidf("tile_image cannot tell how many pixels imageSize %s has; omit imageSize to plan automatically, or use one of %s", size, strings.Join(sizes, ", "))
	}
	if size != "" && !stitchable(size) {
		return nil, apperr.Invalidf("imageSize %s tiles (about %d megapixels) are larger than stitch_tiles accepts (%d); omit imageSize to plan automatically, or use one of %s", size, imageSizePixels(size)/1_000_000, maxTilePixels/1_000_000, strings.Join(sizes, ", "))
	}
	ratios := tiles.ParseRatios(m.Capabilities.AspectRatios)
	if len(ratios) == 0 {
		return nil, apperr.Invalidf("the catalog lists no aspect ratios for %s, so tiles cannot be shaped for it; pick nb2 or pro", m.ID)
	}
	// Decoding and cutting a large image takes seconds: stop between the
	// steps once the request is gone.
	stopped := func() error {
		if err := ctx.Err(); err != nil {
			return requestStopped(err, "tile_image", "it takes a few seconds on large images")
		}
		return nil
	}
	if err := stopped(); err != nil {
		return nil, err
	}

	in, err := s.loadLocal(req.Image, "image")
	if err != nil {
		return nil, err
	}
	imageSum := sha(in.Data)
	// A stitch_tiles result carries the pass, original and reference, as
	// long as it still holds the bytes stitch_tiles wrote.
	pass, original, reference, jobName := 1, strings.TrimSpace(req.Original), "", ""
	prevNative := 0 // model-rendered long edge the image already carries
	if name, ok := s.outputName(req.Image); ok {
		if p, err := s.store.Provenance(name); err == nil && p.Tool == "stitch_tiles" {
			if sum, _ := p.Params["sha256"].(string); sum != imageSum {
				warnings = append(warnings, fmt.Sprintf("%s changed since stitch_tiles wrote it, so it is tiled as a new first pass", req.Image))
			} else {
				pass = intParam(p.Params, "pass") + 1
				prevNative = intParam(p.Params, "nativeLongEdge")
				jobName, _ = p.Params["name"].(string)
				if original != "" {
					warnings = append(warnings, "original is ignored on refinement passes: their crops are edited alone, and the job keeps the first pass's original")
				}
				original, _ = p.Params["original"].(string)
				reference, _ = p.Params["reference"].(string)
			}
		}
	}
	if original == "" {
		original = req.Image
	}
	// Each tile is edited with its crop plus, on the first pass only, the
	// reference.
	editInputs := 1
	if pass == 1 {
		editInputs = 2
	}
	if _, err := m.Validate(catalog.Params{"imageSize": size, "referenceImages": strconv.Itoa(editInputs)}, s.backend()); err != nil {
		return nil, err
	}

	// A separate original only feeds the small reference copy: shrink it
	// before the image is decoded, so the two are never held full size
	// together.
	var refSrc image.Image
	if pass == 1 && original != req.Image {
		oin, err := s.loadLocal(original, "original")
		if err != nil {
			return nil, err
		}
		var decoded int
		if refSrc, decoded, err = decodeShrunk(oin, "original", maxPlanPixels, maxCropPixels); err != nil {
			return nil, err
		}
		if decoded > 16_000_000 {
			runtime.GC() // release a large full-size decode before the image's
		}
		if err := stopped(); err != nil {
			return nil, err
		}
	}
	img, err := decodeImage(in, "image", maxPlanPixels)
	if err != nil {
		return nil, err
	}
	if err := stopped(); err != nil {
		return nil, err
	}
	pw, ph := img.Bounds().Dx(), img.Bounds().Dy()
	if refSrc == nil {
		refSrc = img
	}

	outW, outH := pw, ph
	if pass == 1 {
		long := req.LongEdge
		if long == 0 {
			long = defaultLongEdge
		}
		if long < minLongEdge || long > maxLongEdge {
			return nil, apperr.Invalidf("longEdge must be between %d and %d (got %d)", minLongEdge, maxLongEdge, req.LongEdge)
		}
		outW, outH = fitLong(pw, ph, long)
		if outW*outH > maxCanvasPixels {
			return nil, &apperr.Error{Kind: apperr.Invalid,
				Message: fmt.Sprintf("a %d px long edge makes a %dx%d image (%.0f megapixels); the limit is %d", long, outW, outH, float64(outW*outH)/1e6, maxCanvasPixels/1_000_000),
				Hint:    "Lower longEdge."}
		}
		if long < max(pw, ph) {
			warnings = append(warnings, fmt.Sprintf("longEdge %d is smaller than the image (%dx%d): the result will be downscaled", long, pw, ph))
		}
	} else if req.LongEdge != 0 && req.LongEdge != max(pw, ph) {
		warnings = append(warnings, fmt.Sprintf("longEdge is ignored on refinement passes; the result keeps this image's %dx%d", pw, ph))
	}

	var planned []tiles.Tile
	mode, grid, planNote := "grid", req.Grid, ""
	if len(req.Regions) > 0 {
		if size == "" {
			size = largestEditSize(sizes)
		}
		mode = "regions"
		regions := make([]tiles.Region, len(req.Regions))
		for i, rg := range req.Regions {
			label := slugLabel(rg.Label)
			if label == "" {
				label = fmt.Sprintf("region%d", i+1)
			}
			regions[i] = tiles.Region{X: rg.X, Y: rg.Y, W: rg.Width, H: rg.Height, Label: label}
		}
		planned, err = tiles.Regions(pw, ph, regions, padding, ratios)
	} else if pass == 1 && (grid == 0 || size == "") {
		grids, try := []int{grid}, sizes
		if grid == 0 {
			grids = []int{1, 2, 3, 4}
		}
		if size != "" {
			try = []string{size}
		}
		var p *tilePlan
		if p, err = s.autoPlan(m, location, pw, ph, max(outW, outH), padding, ratios, grids, try); err == nil {
			grid, size, planned, planNote = p.grid, p.size, p.tiles, p.note
		}
	} else {
		if grid == 0 {
			grid = 3
		}
		if size == "" {
			size = largestEditSize(sizes)
		}
		planned, err = tiles.Grid(pw, ph, grid, padding, ratios)
	}
	if err != nil {
		return nil, apperr.Invalidf("%v", err)
	}
	for _, t := range planned {
		// Cutting a crop allocates all of it, mirrored padding included; an
		// image far more elongated than the model's widest ratio would need
		// a crop many times its own size.
		if t.Box.W*t.Box.H > maxPlanPixels {
			return nil, &apperr.Error{Kind: apperr.Invalid,
				Message: fmt.Sprintf("the %dx%d image is too elongated for %s's aspect ratios: tile %d would need a %dx%d crop", pw, ph, m.ID, t.Index, t.Box.W, t.Box.H),
				Hint:    "Cut the image into less elongated parts (no wider than the model's widest aspect ratio, e.g. 8:1 for nb2) and upscale each."}
		}
	}
	boxes := make([]tiles.Box, len(planned))
	for i, t := range planned {
		boxes[i] = t.Box
	}
	if px := footprintPixels(boxes, pw, ph, outW, outH); px > maxFootprintPixels {
		return nil, &apperr.Error{Kind: apperr.Invalid,
			Message: fmt.Sprintf("the %d tiles together cover %.0f megapixels of output, more than stitch_tiles blends (%d)", len(planned), px/1e6, maxFootprintPixels/1_000_000),
			Hint:    "Lower padding, use fewer or smaller regions, or a smaller longEdge."}
	}

	name := slugLabel(req.OutputName)
	if name == "" {
		name = jobName
	}
	if name == "" {
		name = baseName(original)
	}
	if name == "" {
		name = "upscale"
	}
	prefix := fmt.Sprintf("%s-p%d", name, pass)

	// stitch_tiles reloads the tiled image, and job files and provenance
	// record references, so data: URIs are saved and paths made absolute.
	imageRef, err := s.durableRef(req.Image, in, prefix+"-source")
	if err != nil {
		return nil, err
	}
	if original == req.Image {
		original = imageRef
	} else if strings.HasPrefix(original, "data:") {
		oin, err := s.loadLocal(original, "original")
		if err != nil {
			return nil, err
		}
		if original, err = s.durableRef(original, oin, name+"-original"); err != nil {
			return nil, err
		}
	}

	// Only the first pass sends the original along: on refinement passes the
	// crop already carries the identity, and in live tests a whole-photo
	// reference next to a close-up made the model redraw the whole photo.
	var refAsset *store.Asset
	if pass == 1 {
		if refAsset, err = s.tileReference(original, name, refSrc); err != nil {
			return nil, err
		}
		reference = refAsset.URI
	}

	progress(ctx, fmt.Sprintf("cutting %d crops", len(planned)), 0, float64(len(planned)))
	res := &TileImageResult{
		Pass: pass, Mode: mode, PlanNote: planNote, Image: Size{pw, ph}, Output: Size{outW, outH},
		Reference: refAsset, Model: m.ID, ImageSize: size, Warnings: warnings,
	}
	job := tileJob{
		Version: 1, Pass: pass, Mode: mode, Name: name, Image: imageRef, ImageSHA256: imageSum,
		Width: pw, Height: ph, OutWidth: outW, OutHeight: outH,
		Original: original, Reference: reference, Model: m.ID, ImageSize: size, PrevNative: prevNative, CreatedAt: s.now().UTC(),
	}
	// When the image already holds model-rendered detail at its full
	// resolution, re-rendering regions can fix content but not add detail.
	saturated := prevNative >= max(pw, ph)
	if saturated {
		res.Warnings = append(res.Warnings, fmt.Sprintf("this image already carries model-rendered detail at full resolution (about %d px for its %d px long edge), so a refinement pass adds no resolution: use it only to fix visible defects (mismatched eyes, garbled hands or text) with a few targeted regions", prevNative, max(pw, ph)))
	}
	native := math.Inf(1)
	for i, t := range planned {
		if err := ctx.Err(); err != nil {
			return nil, requestStopped(err, "tile_image", "it takes a few seconds on large images")
		}
		crop := cropImage(img, t.Box, maxCropPixels)
		data, err := encodePNG(crop)
		if err != nil {
			return nil, err
		}
		a, err := s.store.Save("image", fmt.Sprintf("%s-t%d-%s", prefix, t.Index, t.Label), "png", data, "image/png", &store.Provenance{
			// The model is recorded so edit_image defaults to it for this crop.
			Tool: "tile_image", Model: m.ID, Inputs: []string{imageRef},
			Params: map[string]any{"box": t.Box, "aspectRatio": t.AspectRatio, "pass": pass},
		})
		if err != nil {
			return nil, s.saveFailed("tile_image", fmt.Sprintf("crop %d", t.Index), err)
		}
		res.Tiles = append(res.Tiles, PlannedTile{Tile: t.Index, Label: t.Label, Box: t.Box, AspectRatio: t.AspectRatio, Outside: t.Outside, Crop: *a})
		job.Tiles = append(job.Tiles, jobTile{Tile: t.Index, Label: t.Label, Box: t.Box, Core: t.Core, AspectRatio: t.AspectRatio, Crop: a.URI})
		// Output pixels per tiled-image pixel, if the tile comes back at size.
		outPx := float64(imageSizePixels(size))
		tw := math.Sqrt(outPx * float64(t.Box.W) / float64(t.Box.H))
		native = math.Min(native, tw/float64(t.Box.W))
		if mode == "regions" && !saturated && tw/float64(t.Box.W) < minRegionGain {
			res.Warnings = append(res.Warnings, fmt.Sprintf("region %d (%s) is %dx%d px: at %s the model renders it at only about %.1fx (and sees the crop at %d px at most), so it adds little detail; use tighter regions, about a third of the image's long side or less", t.Index, t.Label, t.Box.W, t.Box.H, size, tw/float64(t.Box.W), maxCropPixels))
		}
		progress(ctx, fmt.Sprintf("%d/%d crops saved", i+1, len(planned)), float64(i+1), float64(len(planned)))
	}
	if mode == "grid" {
		res.Grid = grid
	}
	if mode == "grid" && !math.IsInf(native, 1) {
		res.NativeLongEdge = int(math.Round(native * float64(max(pw, ph))))
		if res.NativeLongEdge < max(outW, outH) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("the tiles carry about %d px of model detail along the long edge; the %d px output is interpolated beyond that (use a finer grid or a smaller longEdge)", res.NativeLongEdge, max(outW, outH)))
		}
	}

	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return nil, err
	}
	ja, err := s.store.Save("tiles", prefix+"-tiles", "json", data, "application/json", nil)
	if err != nil {
		return nil, s.saveFailed("tile_image", "the tile job", err)
	}
	res.Job = ja.URI

	per := m.EstimateImage(size, 1, 1200, editInputs, s.backend(), location)
	res.Cost = Cost{
		EstimatedUSD: round4(per.USD * float64(len(planned))), USD: round4(per.USD * float64(len(planned))),
		Basis: per.Basis, PriceAsOf: per.PriceAsOf,
		Breakdown: fmt.Sprintf("%d tiles x $%.4f (%s)", len(planned), per.USD, per.Breakdown),
	}
	if refAsset != nil {
		res.Next = fmt.Sprintf("Edit every tile with edit_image (image = the tile's crop uri, referenceImages = [%s], aspectRatio = the tile's aspectRatio, imageSize = %s, model = %s), in parallel if you can; then call stitch_tiles with job = %s and the results.", refAsset.URI, size, m.ID, ja.URI)
	} else {
		res.Next = fmt.Sprintf("Edit every tile with edit_image on its own, without referenceImages (image = the tile's crop uri, aspectRatio = the tile's aspectRatio, imageSize = %s, model = %s), in parallel if you can; then call stitch_tiles with job = %s and the results.", size, m.ID, ja.URI)
	}
	return res, nil
}

// durableRef returns a reference to in that stays valid after this call:
// output URIs as they are, paths made absolute, data: URIs saved as files.
func (s *Service) durableRef(ref string, in *store.Input, saveAs string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "data:"):
		a, err := s.store.Save("image", saveAs, store.ExtFromMIME(in.MIMEType), in.Data, in.MIMEType, nil)
		if err != nil {
			return "", s.saveFailed("tile_image", "the inline image as "+saveAs, err)
		}
		return a.URI, nil
	case strings.HasPrefix(ref, store.URIScheme), strings.HasPrefix(ref, "file://"):
		return ref, nil // already absolute
	}
	if name, ok := s.outputName(ref); ok {
		if _, _, err := s.store.Open(name); err == nil {
			return store.URIScheme + name, nil
		}
	}
	if abs, err := filepath.Abs(expandTilde(ref)); err == nil {
		return abs, nil
	}
	return ref, nil
}

// tileReference saves the copy of the original sent with every first-pass
// tile, made from src: the shrunk original, or the image when it is the
// original.
func (s *Service) tileReference(original, name string, src image.Image) (*store.Asset, error) {
	ref := cropImage(src, tiles.Box{W: src.Bounds().Dx(), H: src.Bounds().Dy()}, maxCropPixels)
	data, err := encodePNG(ref)
	if err != nil {
		return nil, err
	}
	a, err := s.store.Save("image", name+"-reference", "png", data, "image/png", &store.Provenance{Tool: "tile_image", Inputs: []string{original}})
	if err != nil {
		return nil, s.saveFailed("tile_image", "the reference image", err)
	}
	return a, nil
}

// StitchTile is one edited tile.
type StitchTile struct {
	Tile  int    `json:"tile" jsonschema:"Tile number from tile_image."`
	Image string `json:"image" jsonschema:"The edit_image result for that tile: gemini-media:// URI, file name or path."`
}

// DetailPoint is a point as fractions of the image.
type DetailPoint struct {
	X float64 `json:"x" jsonschema:"0-1 from the left"`
	Y float64 `json:"y" jsonschema:"0-1 from the top"`
}

// StitchTilesRequest is the input of stitch_tiles.
type StitchTilesRequest struct {
	Job        string        `json:"job" jsonschema:"The job uri returned by tile_image."`
	Tiles      []StitchTile  `json:"tiles" jsonschema:"The edited tiles. Tiles left out keep the underlying image."`
	Feather    float64       `json:"feather,omitempty" jsonschema:"Blend width as a fraction of each tile's shorter side (default 0.2, 0.05-0.4). Between neighboring tiles it is capped at half their overlap."`
	ColorMatch *bool         `json:"colorMatch,omitempty" jsonschema:"Match each tile's broad exposure and color to the image (default true). Fine texture is never transferred."`
	Details    []DetailPoint `json:"details,omitempty" jsonschema:"Up to 6 points to return as 100% crops for inspection. Default: where grid tiles meet, or each region's center."`
	OutputName string        `json:"outputName,omitempty" jsonschema:"Optional base name for the stitched PNG."`
}

// TileReport is the outcome of one tile.
type TileReport struct {
	Tile   int     `json:"tile"`
	Label  string  `json:"label"`
	Status string  `json:"status" jsonschema:"placed, rejected (prior pixels kept) or unedited"`
	Match  float64 `json:"match,omitempty" jsonschema:"Structural match with the image at its own resolution (1 = identical)"`
	ScaleX float64 `json:"scaleX,omitempty"`
	ScaleY float64 `json:"scaleY,omitempty"`
	ShiftX float64 `json:"shiftX,omitempty" jsonschema:"Correction applied, in output pixels"`
	ShiftY float64 `json:"shiftY,omitempty"`
	Note   string  `json:"note,omitempty"`
}

// StitchResult is the output of stitch_tiles.
type StitchResult struct {
	File           store.Asset  `json:"file"`
	Pass           int          `json:"pass"`
	Tiles          []TileReport `json:"tiles"`
	NativeLongEdge int          `json:"nativeLongEdge,omitempty" jsonschema:"About how many pixels of model-rendered detail the long edge carries, limited by the least detailed tile; 0 when unknown or when part of the image has none"`
	Original       string       `json:"original"`
	Reference      string       `json:"reference"`
	Details        []string     `json:"details,omitempty" jsonschema:"What each detail preview shows, in order"`
	Next           string       `json:"next"`
	Warnings       []string     `json:"warnings,omitempty"`
	// Previews are JPEGs: the whole image, then the 100% details.
	Previews [][]byte `json:"-"`
}

// StitchTiles registers the edited tiles of a tile_image job against the
// tiled image and blends them into one larger image. It makes no model calls.
func (s *Service) StitchTiles(ctx context.Context, req StitchTilesRequest) (*StitchResult, error) {
	if strings.TrimSpace(req.Job) == "" {
		return nil, apperr.Invalidf("job is required: pass the job uri tile_image returned")
	}
	feather := req.Feather
	if feather == 0 {
		feather = 0.2
	}
	if feather < 0.05 || feather > 0.4 {
		return nil, apperr.Invalidf("feather must be between 0.05 and 0.4 (got %g)", req.Feather)
	}
	if len(req.Details) > maxDetails {
		return nil, apperr.Invalidf("at most %d detail points (got %d)", maxDetails, len(req.Details))
	}
	jin, err := s.loadLocal(req.Job, "job")
	if err != nil {
		return nil, err
	}
	var job tileJob
	if err := json.Unmarshal(jin.Data, &job); err != nil || job.Version != 1 || len(job.Tiles) == 0 {
		return nil, apperr.Invalidf("%s is not a tile_image job", req.Job)
	}
	if err := job.check(); err != nil {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("%s is not a usable tile_image job: %v", req.Job, err), Hint: "Run tile_image again and pass the job uri it returns unchanged."}
	}
	byIndex := map[int]jobTile{}
	for _, t := range job.Tiles {
		byIndex[t.Tile] = t
	}
	edited := map[int]string{}
	for _, t := range req.Tiles {
		if _, ok := byIndex[t.Tile]; !ok {
			return nil, apperr.Invalidf("tile %d is not in this job (it has tiles 1-%d)", t.Tile, len(job.Tiles))
		}
		if _, dup := edited[t.Tile]; dup {
			return nil, apperr.Invalidf("tile %d is listed twice", t.Tile)
		}
		if strings.TrimSpace(t.Image) == "" {
			return nil, apperr.Invalidf("tile %d has no image", t.Tile)
		}
		edited[t.Tile] = t.Image
	}
	if len(edited) == 0 {
		return nil, apperr.Invalidf("no tiles given: pass the edit_image result of each tile")
	}

	if err := ctx.Err(); err != nil {
		return nil, requestStopped(err, "stitch_tiles", "an 8K stitch takes 10-30 s")
	}
	pin, err := s.loadLocal(job.Image, "tiled image")
	if err != nil {
		return nil, err
	}
	if sha(pin.Data) != job.ImageSHA256 {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("%s changed after tile_image cut it", job.Image), Hint: "Run tile_image again on the current file."}
	}
	plan, err := decodeImage(pin, "tiled image", maxPlanPixels)
	if err != nil {
		return nil, err
	}
	// From here stitching holds several hundred MB for a few seconds; hand
	// it back to the OS afterwards rather than keeping it in a long-lived
	// server (not on the cheap rejections above).
	defer debug.FreeOSMemory()
	if plan.Bounds().Dx() != job.Width || plan.Bounds().Dy() != job.Height {
		return nil, apperr.Invalidf("%s is %dx%d, not the %dx%d tile_image cut", job.Image, plan.Bounds().Dx(), plan.Bounds().Dy(), job.Width, job.Height)
	}
	plan = normalizeRGBA(plan)

	opt := tiles.Options{NoColorMatch: req.ColorMatch != nil && !*req.ColorMatch}
	sx := float64(job.OutWidth) / float64(job.Width)
	sy := float64(job.OutHeight) / float64(job.Height)
	res := &StitchResult{Pass: job.Pass, Original: job.Original, Reference: job.Reference}
	// Align the edited tiles, decoding a few at a time (a 4K tile decodes
	// to about 70 MB).
	type aligned struct {
		a    tiles.Alignment
		w, h int
		sum  string // sha256 of the bytes aligned, checked when reloaded
		err  error
	}
	results := make([]aligned, len(job.Tiles))
	var wg sync.WaitGroup
	sem := make(chan struct{}, alignWorkers)
	var mu sync.Mutex
	done := 0
	total := float64(len(edited))
	for i, t := range job.Tiles {
		ref, ok := edited[t.Tile]
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return // reported after the wait
			}
			tile, sum, err := s.loadTile(ref, t.Tile)
			if err != nil {
				results[i].err = err
				return
			}
			results[i] = aligned{a: tiles.Align(plan, t.Box, tile, opt), w: tile.Bounds().Dx(), h: tile.Bounds().Dy(), sum: sum}
			mu.Lock()
			done++
			progress(ctx, fmt.Sprintf("aligned %d/%d tiles", done, len(edited)), float64(done), total*2)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, requestStopped(err, "stitch_tiles", "an 8K stitch takes 10-30 s")
	}

	// source is a placed tile's file, reloaded for painting.
	type source struct {
		ref, sum string
		tile     int
		w, h     int // decoded size
	}
	var placements []tiles.Placement
	var sources []source
	native := math.Inf(1)
	for i, t := range job.Tiles {
		ref, ok := edited[t.Tile]
		if !ok {
			res.Tiles = append(res.Tiles, TileReport{Tile: t.Tile, Label: t.Label, Status: "unedited"})
			continue
		}
		if results[i].err != nil {
			return nil, results[i].err
		}
		a := results[i].a
		rep := TileReport{Tile: t.Tile, Label: t.Label, Status: a.Status, Match: round3(a.Match), Note: a.Note}
		if a.Status == tiles.Placed {
			rep.ScaleX, rep.ScaleY = round4(a.ScaleX), round4(a.ScaleY)
			rep.ShiftX, rep.ShiftY = round1(a.ShiftX*sx), round1(a.ShiftY*sy)
			placements = append(placements, tiles.Placement{Box: t.Box, Align: a})
			sources = append(sources, source{ref: ref, sum: results[i].sum, tile: t.Tile, w: results[i].w, h: results[i].h})
			// Rendered pixels per tiled-image pixel, along the sparser axis.
			native = math.Min(native, math.Min(float64(results[i].w)/(a.ScaleX*float64(t.Box.W)), float64(results[i].h)/(a.ScaleY*float64(t.Box.H))))
		}
		res.Tiles = append(res.Tiles, rep)
	}
	if len(placements) == 0 {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: "no tile could be aligned with the image: " + rejectSummary(res.Tiles),
			Hint: "Check that each edit kept the crop's framing (pass the tile's aspectRatio and say 'return exactly this crop'), then retry those tiles."}
	}

	tiles.Feathers(placements, job.Width, job.Height, feather)
	canvas := tiles.NewCanvas(plan, job.OutWidth, job.OutHeight)
	canvas.Reserve(placements)
	// Prepare (decode and resample) the next tile while painting this one
	// when the two fit maxPreparedBytes together; otherwise wait until the
	// previous tile is painted. A tile holds its decode, a converted copy
	// and its resampled window.
	need := make([]int, len(placements))
	for i, p := range placements {
		need[i] = 8*sources[i].w*sources[i].h + 4*canvas.PreparedPixels(p)
		if need[i] > maxPreparedBytes {
			return nil, &apperr.Error{Kind: apperr.Invalid,
				Message: fmt.Sprintf("tile %d would need about %d MB to blend, more than stitch_tiles allows (%d MB)", sources[i].tile, need[i]>>20, maxPreparedBytes>>20),
				Hint:    "Run tile_image again with a smaller longEdge or a finer grid, so each tile covers less of the output."}
		}
	}
	type prepared struct {
		p   *tiles.Prepared
		err error
	}
	next := make(chan prepared)                   // unbuffered: one prepared tile waits at most
	freed := make(chan struct{}, len(placements)) // one per tile taken off next
	go func() {
		defer close(next)
		taken := 0
		for i, p := range placements {
			if i > 0 && need[i-1]+need[i] > maxPreparedBytes {
				for ; taken < i; taken++ {
					<-freed
				}
				runtime.GC() // return the painted tile's memory first
			}
			if ctx.Err() != nil {
				return
			}
			src := sources[i]
			tile, sum, err := s.loadTile(src.ref, src.tile)
			if err == nil && sum != src.sum {
				// Its alignment and color match belong to the old pixels.
				err = &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("tile %d (%s) changed while stitch_tiles was running", src.tile, src.ref),
					Hint: "Call stitch_tiles again once nothing is writing to the tile files."}
			}
			if err != nil {
				next <- prepared{err: err}
				return
			}
			next <- prepared{p: canvas.Prepare(p, tile)}
		}
	}()
	painted := 0
	for pr := range next {
		if pr.err != nil {
			for range next { // let the producer finish
			}
			return nil, pr.err
		}
		if ctx.Err() == nil { // else drain: the producer stops before its next tile
			canvas.Paint(pr.p)
			painted++
			progress(ctx, fmt.Sprintf("blended %d/%d tiles", painted, len(placements)), total+float64(painted), total*2)
		}
		freed <- struct{}{}
	}

	if err := ctx.Err(); err != nil {
		return nil, requestStopped(err, "stitch_tiles", "an 8K stitch takes 10-30 s")
	}
	progress(ctx, "encoding PNG", total*2, total*2)
	out := canvas.Finish()
	data, err := encodePNG(out)
	if err != nil {
		return nil, err
	}
	name := slugLabel(req.OutputName)
	if name == "" {
		name = job.Name + "-upscaled"
		if job.Pass > 1 {
			name += fmt.Sprintf("-p%d", job.Pass)
		}
	}
	switch {
	case job.Mode != "grid":
		// Regions keep the image's figure unless one is rendered coarser
		// (a smaller imageSize or a large crop), which then caps it.
		res.NativeLongEdge = min(job.PrevNative, int(math.Round(native*float64(max(job.Width, job.Height)))))
	case len(placements) == len(job.Tiles):
		res.NativeLongEdge = int(math.Round(native * float64(max(job.Width, job.Height))))
	default:
		// Areas without a placed tile are interpolated, so the image as a
		// whole carries no model detail figure; 0 keeps later passes from
		// treating it as saturated.
		res.NativeLongEdge = 0
	}
	inputs := []string{job.Image}
	for _, src := range sources {
		inputs = append(inputs, provenanceRef(src.ref))
	}
	// tile_image reads the pass, original and reference back from the
	// provenance, so a stitched image without it is not reported.
	asset, err := s.store.SaveWithProvenance("image", name, "png", data, "image/png", &store.Provenance{
		Tool: "stitch_tiles", Model: job.Model, Inputs: inputs,
		// tile_image trusts this record only for these exact bytes.
		Params: map[string]any{"pass": job.Pass, "name": job.Name, "original": job.Original, "reference": job.Reference, "job": req.Job, "nativeLongEdge": res.NativeLongEdge, "sha256": sha(data)},
	})
	if err != nil {
		return nil, s.saveFailed("stitch_tiles", "the stitched image", err)
	}
	res.File = *asset

	if s.cfg.InlinePreviewsEnabled() {
		var prev bytes.Buffer
		thumb := cropImage(out, tiles.Box{W: job.OutWidth, H: job.OutHeight}, s.cfg.PreviewMaxPixels)
		if err := jpeg.Encode(&prev, thumb, &jpeg.Options{Quality: 80}); err == nil {
			res.Previews = append(res.Previews, prev.Bytes())
		}
		for _, d := range detailPoints(req.Details, job, placements) {
			cx, cy := int(d.X*float64(job.OutWidth)), int(d.Y*float64(job.OutHeight))
			b := tiles.Box{X: cx - detailPixels/2, Y: cy - detailPixels/2, W: detailPixels, H: detailPixels}
			b.X = min(max(b.X, 0), max(job.OutWidth-b.W, 0))
			b.Y = min(max(b.Y, 0), max(job.OutHeight-b.H, 0))
			b.W, b.H = min(b.W, job.OutWidth), min(b.H, job.OutHeight)
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, cropImage(out, b, 0), &jpeg.Options{Quality: 85}); err == nil {
				res.Previews = append(res.Previews, buf.Bytes())
				res.Details = append(res.Details, fmt.Sprintf("100%% crop at %.2f,%.2f (%d,%d px)", d.X, d.Y, b.X, b.Y))
			}
		}
	}

	rejected := 0
	var unedited []string
	for _, t := range res.Tiles {
		switch t.Status {
		case tiles.Rejected:
			rejected++
		case "unedited":
			unedited = append(unedited, fmt.Sprintf("%d (%s)", t.Tile, t.Label))
		}
	}
	if len(unedited) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("tile(s) %s were not passed and keep the underlying image there", strings.Join(unedited, ", ")))
	}
	switch {
	case rejected > 0:
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d tile(s) were rejected and keep the underlying image: %s", rejected, rejectSummary(res.Tiles)))
		res.Next = "Retry the rejected tiles with edit_image (same crop, reference and aspectRatio; insist on returning exactly the crop's framing), at most twice each, then call stitch_tiles again with every tile. If a tile keeps failing, deliver without it and say so."
	case len(unedited) > 0 && job.Mode == "grid":
		res.Next = "Edit the missing tiles with edit_image (same crop, reference and aspectRatio), then call stitch_tiles again with every tile. If you deliver without them, say which areas are only interpolated."
	case job.Pass == 1 && res.NativeLongEdge >= max(job.OutWidth, job.OutHeight):
		res.Next = fmt.Sprintf("Inspect the preview and the 100%% details for seams, doubling or drift, and for defects such as mismatched eyes or garbled hands or text. The tiles already carry detail at full resolution, so another pass adds no resolution: only to fix a visible defect, call tile_image with image = %s and a few targeted regions, then edit those crops without referenceImages. Otherwise deliver %s.", asset.URI, asset.Path)
	case job.Pass == 1:
		res.Next = fmt.Sprintf("Inspect the preview and the 100%% details for seams, doubling or drift. For a refinement pass, call tile_image with image = %s and tight regions (each about a third of the image or less) for faces, hands, text or fine materials, then edit those crops without referenceImages; otherwise deliver %s.", asset.URI, asset.Path)
	default:
		res.Next = fmt.Sprintf("Inspect the preview and details; run another regions pass for tighter features if needed, otherwise deliver %s.", asset.Path)
	}
	if res.NativeLongEdge > 0 && res.NativeLongEdge < max(job.OutWidth, job.OutHeight) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("the tiles carry about %d px of model detail along the long edge; the %d px output is interpolated beyond that", res.NativeLongEdge, max(job.OutWidth, job.OutHeight)))
	}
	return res, nil
}

// saveFailed reports that tool could not write what into the output
// directory; nothing was spent, so the call can simply be repeated.
func (s *Service) saveFailed(tool, what string, err error) error {
	return &apperr.Error{Kind: apperr.Unknown, Message: fmt.Sprintf("%s could not save %s: %v", tool, what, err),
		Hint: fmt.Sprintf("Make the output directory %s and its .meta folder writable, with free space, then call %s again; it costs nothing.", s.store.Dir(), tool), Cause: err}
}

// requestStopped reports local work abandoned because its request ended;
// duration says how long the tool takes, for the timeout hint.
func requestStopped(err error, tool, duration string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &apperr.Error{Kind: apperr.Timeout, Message: tool + " stopped: the request timed out", Hint: fmt.Sprintf("Call %s again with a longer client timeout; %s.", tool, duration), Cause: err}
	}
	return &apperr.Error{Kind: apperr.Canceled, Message: tool + " stopped: the request was canceled", Hint: "Call " + tool + " again when you still want the result.", Cause: err}
}

// tilePlan is a grid and edit size chosen by autoPlan.
type tilePlan struct {
	grid   int
	size   string
	tiles  []tiles.Tile
	native int // model-rendered long edge if every tile comes back at size
	cost   float64
	note   string
}

// autoPlan picks the cheapest grid and edit size whose tiles carry at least
// target pixels of model-rendered detail along the long edge, or the most
// detailed plan when none reaches it.
func (s *Service) autoPlan(m *catalog.Model, location string, pw, ph, target int, padding float64, ratios []tiles.Ratio, grids []int, sizes []string) (*tilePlan, error) {
	var best *tilePlan
	var lastErr error
	meets := func(p *tilePlan) bool { return float64(p.native) >= 0.97*float64(target) }
	for _, g := range grids {
		for _, size := range sizes {
			planned, err := tiles.Grid(pw, ph, g, padding, ratios)
			if err != nil {
				lastErr = err
				continue
			}
			p := &tilePlan{grid: g, size: size, tiles: planned, native: nativeLongEdge(planned, size, pw, ph)}
			p.cost = float64(len(planned)) * m.EstimateImage(size, 1, 1200, 2, s.backend(), location).USD
			switch {
			case best == nil:
				best = p
			case meets(p) && !meets(best), meets(p) && meets(best) && (p.cost < best.cost-1e-9 || math.Abs(p.cost-best.cost) < 1e-9 && p.native > best.native):
				best = p
			case !meets(p) && !meets(best) && (p.native > best.native || p.native == best.native && p.cost < best.cost):
				best = p
			}
		}
	}
	if best == nil {
		return nil, lastErr
	}
	tilesWord := "tiles"
	if len(best.tiles) == 1 {
		tilesWord = "tile"
	}
	best.note = fmt.Sprintf("%d %s at %s (grid %d), the cheapest plan whose detail covers the %d px target", len(best.tiles), tilesWord, best.size, best.grid, target)
	if !meets(best) {
		best.note = fmt.Sprintf("%d %s at %s (grid %d), the most detailed plan available; it falls short of the %d px target", len(best.tiles), tilesWord, best.size, best.grid, target)
	}
	return best, nil
}

// nativeLongEdge is about how many pixels of model-rendered detail the long
// edge of a pw x ph image carries if every tile comes back at size.
func nativeLongEdge(ts []tiles.Tile, size string, pw, ph int) int {
	native := math.Inf(1)
	for _, t := range ts {
		tw := math.Sqrt(float64(imageSizePixels(size)) * float64(t.Box.W) / float64(t.Box.H))
		native = math.Min(native, tw/float64(t.Box.W))
	}
	if math.IsInf(native, 1) {
		return 0
	}
	return int(math.Round(native * float64(max(pw, ph))))
}

// editSizes are the model's output sizes worth planning with: those whose
// pixel count is known, above 512 (too small to add detail) and that
// stitch_tiles can load.
func editSizes(m *catalog.Model) []string {
	var out []string
	for _, s := range m.Capabilities.ImageSizes {
		if imageSizePixels(s) > 512*512 && stitchable(s) {
			out = append(out, s)
		}
	}
	return out
}

// stitchable reports whether tiles edited at size fit stitch_tiles' decode
// limit, with room for the aspect ratios that come back slightly larger.
func stitchable(size string) bool {
	return imageSizePixels(size)*5/4 <= maxTilePixels
}

// largestEditSize is the size in sizes with the most pixels.
func largestEditSize(sizes []string) string {
	return slices.MaxFunc(sizes, func(a, b string) int { return imageSizePixels(a) - imageSizePixels(b) })
}

// detailPoints picks the 100% crops returned for inspection.
func detailPoints(asked []DetailPoint, job tileJob, ps []tiles.Placement) []DetailPoint {
	if len(asked) > 0 {
		out := make([]DetailPoint, len(asked))
		for i, d := range asked {
			out[i] = DetailPoint{X: clamp01(d.X), Y: clamp01(d.Y)}
		}
		return out
	}
	var out []DetailPoint
	if job.Mode == "grid" {
		// Interior corners where four tiles meet: the hardest blends.
		xs, ys := map[int]bool{}, map[int]bool{}
		for _, t := range job.Tiles {
			if t.Core.X > 0 {
				xs[t.Core.X] = true
			}
			if t.Core.Y > 0 {
				ys[t.Core.Y] = true
			}
		}
		for _, y := range slices.Sorted(maps.Keys(ys)) {
			for _, x := range slices.Sorted(maps.Keys(xs)) {
				out = append(out, DetailPoint{X: float64(x) / float64(job.Width), Y: float64(y) / float64(job.Height)})
			}
		}
	}
	if len(out) == 0 {
		for _, p := range ps {
			out = append(out, DetailPoint{
				X: (float64(p.Box.X) + float64(p.Box.W)/2) / float64(job.Width),
				Y: (float64(p.Box.Y) + float64(p.Box.H)/2) / float64(job.Height),
			})
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

// loadLocal reads an input for local processing (not sent to Google), with
// a size limit large enough for stitched images.
func (s *Service) loadLocal(ref, what string) (*store.Input, error) {
	pol := s.inputPolicy()
	pol.MaxBytes = localInputBytes
	in, err := s.store.LoadInput(ref, pol)
	if err != nil {
		return nil, &apperr.Error{Kind: apperr.Invalid, Message: fmt.Sprintf("%s: %v", what, err),
			Hint: "Pass a gemini-media:// URI or file name from an earlier result, or a path inside the allowed directories (get_config shows them).", Cause: err}
	}
	return in, nil
}

// loadTile decodes edited tile index and returns the sha256 of its bytes.
func (s *Service) loadTile(ref string, index int) (image.Image, string, error) {
	what := fmt.Sprintf("tile %d", index)
	in, err := s.loadLocal(ref, what)
	if err != nil {
		return nil, "", err
	}
	img, err := decodeImage(in, what, maxTilePixels)
	if err != nil {
		return nil, "", err
	}
	return img, sha(in.Data), nil
}

// provenanceRef is ref as recorded in provenance: inline data is named by
// its type, like store inputs, rather than copied.
func provenanceRef(ref string) string {
	if mime, ok := strings.CutPrefix(ref, "data:"); ok {
		if i := strings.IndexAny(mime, ";,"); i >= 0 {
			mime = mime[:i]
		}
		return "data:" + mime
	}
	return ref
}

// decodeImage decodes an image of at most limit pixels, checked from its
// header before any pixels are allocated, turned by its EXIF orientation.
func decodeImage(in *store.Input, what string, limit int) (image.Image, error) {
	img, format, err := decodeRaw(in, what, limit)
	if err != nil {
		return nil, err
	}
	if format == "jpeg" {
		img = orient(img, jpegOrientation(in.Data))
	}
	return img, nil
}

// decodeShrunk is decodeImage for an image only needed small: it is
// box-averaged to a long side under 2*maxPx before being turned, a band of
// rows at a time, so it costs its decoded size once. It also returns the
// decoded size in pixels.
func decodeShrunk(in *store.Input, what string, limit, maxPx int) (image.Image, int, error) {
	img, format, err := decodeRaw(in, what, limit)
	if err != nil {
		return nil, 0, err
	}
	b := img.Bounds()
	if f := max(b.Dx(), b.Dy()) / maxPx; f >= 2 {
		img = shrink(img, f)
	}
	if format == "jpeg" {
		img = orient(img, jpegOrientation(in.Data))
	}
	return img, b.Dx() * b.Dy(), nil
}

func decodeRaw(in *store.Input, what string, limit int) (image.Image, string, error) {
	if err := requireImage(in, what); err != nil {
		return nil, "", err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(in.Data))
	if err != nil {
		return nil, "", apperr.Invalidf("%s %s: cannot decode %s (%v); use PNG, JPEG or WebP", what, in.Ref, in.MIMEType, err)
	}
	if cfg.Width*cfg.Height > limit {
		return nil, "", apperr.Invalidf("%s %s is %dx%d; the limit is %d megapixels", what, in.Ref, cfg.Width, cfg.Height, limit/1_000_000)
	}
	img, format, err := image.Decode(bytes.NewReader(in.Data))
	if err != nil {
		return nil, "", apperr.Invalidf("%s %s: %v", what, in.Ref, err)
	}
	return img, format, nil
}

// shrink box-averages img by an integer factor f (dropping the last
// partial block), converting f rows at a time.
func shrink(img image.Image, f int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx()/f, b.Dy()/f
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	band := image.NewRGBA(image.Rect(0, 0, w*f, f))
	sum := make([]uint32, 4*w)
	div := uint32(f * f)
	for y := range h {
		draw.Draw(band, band.Rect, img, image.Pt(b.Min.X, b.Min.Y+y*f), draw.Src)
		clear(sum)
		for r := range f {
			row := band.Pix[r*band.Stride : r*band.Stride+4*w*f]
			for x := range w * f {
				s, p := sum[4*(x/f):4*(x/f)+4], row[4*x:4*x+4]
				s[0], s[1], s[2], s[3] = s[0]+uint32(p[0]), s[1]+uint32(p[1]), s[2]+uint32(p[2]), s[3]+uint32(p[3])
			}
		}
		out := dst.Pix[y*dst.Stride : y*dst.Stride+4*w]
		for i, v := range sum {
			out[i] = uint8((v + div/2) / div)
		}
	}
	return dst
}

// normalizeRGBA returns img as an *image.RGBA at the origin (copying only
// when needed), so it can serve as the canvas of a same-size stitch.
func normalizeRGBA(img image.Image) image.Image {
	if r, ok := img.(*image.RGBA); ok && r.Rect.Min == (image.Point{}) && r.Stride == 4*r.Rect.Dx() {
		return r
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

// cropImage copies box out of img, downscaled so its long side is at most
// maxPx (0 = no limit).
// Parts of box outside the image are mirrored (see tiles.Crop).
func cropImage(img image.Image, box tiles.Box, maxPx int) image.Image {
	b := img.Bounds()
	w, h := box.W, box.H
	if maxPx > 0 && max(w, h) > maxPx {
		w, h = fitLong(w, h, maxPx)
	}
	if box.X >= 0 && box.Y >= 0 && box.X1() <= b.Dx() && box.Y1() <= b.Dy() {
		src := image.Rect(box.X, box.Y, box.X1(), box.Y1()).Add(b.Min)
		if w == box.W && h == box.H {
			dst := image.NewRGBA(image.Rect(0, 0, w, h))
			draw.Draw(dst, dst.Bounds(), img, src.Min, draw.Src)
			return dst
		}
		return tiles.Resize(img, src, w, h)
	}
	c := tiles.Crop(img, box)
	if w == box.W && h == box.H {
		return c
	}
	return tiles.Resize(c, c.Rect, w, h)
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	b := img.Bounds()
	buf.Grow(b.Dx() * b.Dy() * 2) // photos compress to about 2 bytes per pixel
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// fitLong scales w x h so the long side is long, keeping the aspect ratio.
func fitLong(w, h, long int) (int, int) {
	if w >= h {
		return long, max(int(math.Round(float64(h)*float64(long)/float64(w))), 1)
	}
	return max(int(math.Round(float64(w)*float64(long)/float64(h))), 1), long
}

// imageSizePixels is the approximate pixel count of an output size named as
// in the catalog: a square side in pixels ("512") or in multiples of 1024
// ("1K", "4K"). It is 0 for any other name, which no plan may use.
func imageSizePixels(size string) int {
	side := 0
	if k, ok := strings.CutSuffix(size, "K"); ok {
		if n, err := strconv.Atoi(k); err == nil && n > 0 && n <= 16 {
			side = n * 1024
		}
	} else if n, err := strconv.Atoi(size); err == nil && n > 0 && n <= maxLongEdge {
		side = n
	}
	return side * side
}

func rejectSummary(rs []TileReport) string {
	var parts []string
	for _, r := range rs {
		if r.Status == tiles.Rejected {
			parts = append(parts, fmt.Sprintf("tile %d (%s): %s", r.Tile, r.Label, r.Note))
		}
	}
	return strings.Join(parts, "; ")
}

// slugLabel keeps labels safe for file names.
func slugLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		switch {
		case ok:
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}

// baseName is a file-name base for a reference (path, URI or data URI).
func baseName(ref string) string {
	if strings.HasPrefix(ref, "data:") {
		return ""
	}
	base := filepath.Base(strings.TrimPrefix(ref, store.URIScheme))
	return slugLabel(strings.TrimSuffix(base, filepath.Ext(base)))
}

func expandTilde(p string) string {
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func intParam(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func clamp01(v float64) float64 { return math.Min(math.Max(v, 0), 1) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
