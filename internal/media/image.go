package media

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/apperr"
	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/google"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

// MaxImagesPerCall bounds `count` to keep latency and spend predictable.
const MaxImagesPerCall = 4

// ImageRequest is the input of generate_image.
type ImageRequest struct {
	Prompt          string   `json:"prompt" jsonschema:"What to create. Describe subject, setting, composition, lighting and style in full sentences. Put any exact text to render in quotes."`
	Model           string   `json:"model,omitempty" jsonschema:"Model ID or alias. nb2 (default: Nano Banana 2.1, fast and cheap), pro (Nano Banana Pro, highest fidelity for dense scenes), nb2-lite (cheapest inputs). See list_models."`
	AspectRatio     string   `json:"aspectRatio,omitempty" jsonschema:"e.g. 1:1, 3:2, 2:3, 4:3, 3:4, 4:5, 16:9, 9:16, 21:9 (nb2 also 1:4, 4:1, 1:8, 8:1). Defaults to 1:1 when there are no reference images; with them the model usually follows the first image's ratio."`
	ImageSize       string   `json:"imageSize,omitempty" jsonschema:"Output size: 1K (default), 2K or 4K. Larger costs more. 512 only on the deprecated gemini-3.1-flash-image."`
	ReferenceImages []string `json:"referenceImages,omitempty" jsonschema:"Optional images to guide the result (subjects, products, characters, style): file paths, gemini-media:// URIs from earlier results, or data: URIs. Up to 14 for nb2/pro."`
	Count           int      `json:"count,omitempty" jsonschema:"Number of variations to generate in parallel (1-4, default 1). Each is billed."`
	GoogleSearch    bool     `json:"googleSearch,omitempty" jsonschema:"Ground the image in Google Search results (current events, real places, data for infographics)."`
	OutputName      string   `json:"outputName,omitempty" jsonschema:"Optional base file name (without extension) for the saved file(s)."`
	ApprovedCostUSD float64  `json:"approvedCostUsd,omitempty" jsonschema:"Only needed when a call exceeds the configured confirmation threshold: the cost in USD the user approved."`
}

// EditImageRequest is the input of edit_image.
type EditImageRequest struct {
	Image           string   `json:"image" jsonschema:"The image to edit: file path, gemini-media:// URI from an earlier result, or data: URI."`
	Prompt          string   `json:"prompt" jsonschema:"The change to make, stated specifically, plus what must stay the same (e.g. 'Replace the sky with a sunset; keep the people, framing and lighting on faces unchanged')."`
	ReferenceImages []string `json:"referenceImages,omitempty" jsonschema:"Optional extra images to pull elements or style from (e.g. 'put the logo from image 2 on the mug')."`
	Model           string   `json:"model,omitempty" jsonschema:"Model ID or alias. Defaults to the model that created the source image, else nb2."`
	AspectRatio     string   `json:"aspectRatio,omitempty" jsonschema:"Change the aspect ratio (outpainting). Omit to keep the source ratio."`
	ImageSize       string   `json:"imageSize,omitempty" jsonschema:"Output size: 1K, 2K or 4K (512 only on the deprecated gemini-3.1-flash-image)."`
	OutputName      string   `json:"outputName,omitempty" jsonschema:"Optional base file name for the saved file. Defaults to the source name plus -edit."`
	ApprovedCostUSD float64  `json:"approvedCostUsd,omitempty" jsonschema:"Only needed above the confirmation threshold: the cost in USD the user approved."`
}

// ImageResult is the output of generate_image and edit_image.
type ImageResult struct {
	Files []store.Asset `json:"files"`
	Model string        `json:"model"`
	Text  string        `json:"text,omitempty" jsonschema:"Commentary the model returned alongside the image(s)"`
	Cost  Cost          `json:"cost"`
	// Grounding lists the Google Search queries and sources behind the
	// image when googleSearch was used.
	Grounding *Grounding `json:"grounding,omitempty" jsonschema:"Google Search queries the model ran and the pages it used (googleSearch only)"`
	Warnings  []string   `json:"warnings,omitempty"`
	// previews are downscaled JPEGs for inline display (not serialized).
	Previews [][]byte `json:"-"`
}

// Grounding reports Google Search grounding.
type Grounding struct {
	Queries []string        `json:"queries,omitempty"`
	Sources []google.Source `json:"sources,omitempty"`
}

// GenerateImage creates images from text and optional references.
func (s *Service) GenerateImage(ctx context.Context, req ImageRequest) (*ImageResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, apperr.Invalidf("prompt is required")
	}
	return s.runImage(ctx, "generate_image", req.Model, req.Prompt, "", req.ReferenceImages, req.AspectRatio, req.ImageSize, req.Count, req.GoogleSearch, req.OutputName, req.ApprovedCostUSD)
}

// EditImage modifies an existing image.
func (s *Service) EditImage(ctx context.Context, req EditImageRequest) (*ImageResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, apperr.Invalidf("prompt is required")
	}
	if strings.TrimSpace(req.Image) == "" {
		return nil, apperr.Invalidf("image is required")
	}
	model := req.Model
	if model == "" {
		// Keep editing with the model that produced the image, for consistency.
		if name, ok := s.outputName(req.Image); ok {
			if p, err := s.store.Provenance(name); err == nil && p.Model != "" {
				model = p.Model
			}
		}
	}
	outName := req.OutputName
	if outName == "" {
		outName = editName(req.Image)
	}
	return s.runImage(ctx, "edit_image", model, req.Prompt, req.Image, req.ReferenceImages, req.AspectRatio, req.ImageSize, 1, false, outName, req.ApprovedCostUSD)
}

var editSuffix = regexp.MustCompile(`-edit(-\d+)?$`)

// editName names an edit after its source ("portrait.jpg" -> "portrait-edit")
// so edit chains stay readable; the store adds -2, -3... on collision.
func editName(source string) string {
	if strings.HasPrefix(source, "data:") {
		return ""
	}
	base := filepath.Base(strings.TrimPrefix(source, store.URIScheme))
	base = editSuffix.ReplaceAllString(strings.TrimSuffix(base, filepath.Ext(base)), "")
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base + "-edit"
}

func (s *Service) runImage(ctx context.Context, tool, modelName, prompt, source string, refs []string, aspect, size string, count int, search bool, outName string, approved float64) (*ImageResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if count == 0 {
		count = 1
	}
	if count < 1 || count > MaxImagesPerCall {
		return nil, apperr.Invalidf("count must be between 1 and %d", MaxImagesPerCall)
	}
	size = strings.ToUpper(strings.TrimSpace(size))
	if size == "0.5K" {
		size = "512"
	}
	r, location, warnings, err := s.resolve(modelName, catalog.Image)
	if err != nil {
		return nil, err
	}
	m := r.Model

	var inputs []*store.Input
	if source != "" {
		in, err := s.loadInput(source, "source image")
		if err != nil {
			return nil, err
		}
		if err := requireImage(in, "source image"); err != nil {
			return nil, err
		}
		inputs = append(inputs, in)
	}
	refInputs, err := s.loadInputs(refs, "reference image")
	if err != nil {
		return nil, err
	}
	for _, in := range refInputs {
		if err := requireImage(in, "reference image"); err != nil {
			return nil, err
		}
	}
	inputs = append(inputs, refInputs...)
	if aspect == "" && len(inputs) == 0 {
		// Models pick their own default otherwise (Nano Banana 2.1 picks 16:9).
		aspect = "1:1"
	}

	dropped, err := m.Validate(catalog.Params{
		"aspectRatio":     aspect,
		"imageSize":       size,
		"referenceImages": itoa(len(inputs)),
		"googleSearch":    btoa(search),
	}, s.backend())
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, dropWarning(dropped, s.backend())...)
	if slices.Contains(dropped, "googleSearch") {
		search = false
	}

	est := m.EstimateImage(size, count, len(prompt), len(inputs), s.backend(), location)
	res, err := s.reserve(&est, approved)
	if err != nil {
		return nil, err
	}

	contents := []*genai.Content{{Role: string(genai.RoleUser), Parts: imageParts(inputs, prompt)}}
	gcfg := &genai.GenerateContentConfig{ResponseModalities: []string{"IMAGE", "TEXT"}}
	if aspect != "" || size != "" {
		gcfg.ImageConfig = &genai.ImageConfig{AspectRatio: aspect, ImageSize: size}
	}
	if search {
		gcfg.Tools = []*genai.Tool{{GoogleSearch: &genai.GoogleSearch{}}}
	}

	entry := spend.Entry{Tool: tool, Model: m.ID, MediaType: catalog.Image, Units: map[string]any{"count": count, "imageSize": firstNonEmpty(size, m.DefaultImageSize()), "inputImages": len(inputs)}}
	progress(ctx, fmt.Sprintf("generating %d image(s) with %s", count, m.ID), 0, float64(count))

	type outcome struct {
		res *google.Result
		err error
	}
	outcomes := make([]outcome, count)
	var wg sync.WaitGroup
	var done int
	var mu sync.Mutex
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := s.api.GenerateContent(ctx, location, r.APIID, contents, gcfg)
			if err != nil {
				outcomes[i] = outcome{err: google.Classify(err, "image generation", m.ID, s.backend())}
				return
			}
			parsed, perr := google.ParseResponse(resp, "image/")
			outcomes[i] = outcome{res: parsed, err: perr}
			mu.Lock()
			done++
			progress(ctx, fmt.Sprintf("%d/%d images ready", done, count), float64(done), float64(count))
			mu.Unlock()
		}()
	}
	wg.Wait()

	result := &ImageResult{Model: m.ID, Warnings: warnings}
	// usage is everything the API reported; billable leaves out variations
	// blocked by safety filters, which are not charged.
	var usage, billable google.Usage
	var texts []string
	var grounding Grounding
	var firstErr, saveErr error
	succeeded := 0
	for _, o := range outcomes {
		if o.res != nil {
			usage = addUsage(usage, o.res.Usage)
			if apperr.KindOf(o.err) != apperr.Safety {
				billable = addUsage(billable, o.res.Usage)
			}
			result.Warnings = append(result.Warnings, modelNotices(o.res)...)
			grounding.Queries = append(grounding.Queries, o.res.SearchQueries...)
			grounding.Sources = append(grounding.Sources, o.res.Sources...)
		}
		if o.err != nil {
			if firstErr == nil {
				firstErr = o.err
			}
			continue
		}
		succeeded++
		if o.res.Text != "" {
			texts = append(texts, o.res.Text)
		}
		for _, blob := range o.res.Media {
			asset, err := s.store.Save("image", outName, store.ExtFromMIME(blob.MIMEType), blob.Data, blob.MIMEType, &store.Provenance{
				Tool: tool, Model: m.ID, Prompt: prompt, Inputs: refsOf(inputs), ModelText: o.res.Text,
				Params: map[string]any{"aspectRatio": aspect, "imageSize": size, "googleSearch": search},
			})
			if err != nil {
				saveErr = err
				continue
			}
			result.Files = append(result.Files, *asset)
			var prev []byte // aligned with Files; nil when no preview
			if s.cfg.InlinePreviewsEnabled() {
				prev, _ = store.Preview(blob.Data, s.cfg.PreviewMaxPixels)
			}
			result.Previews = append(result.Previews, prev)
		}
	}
	result.Text = strings.Join(dedupe(texts), "\n")
	if search {
		grounding.Queries = dedupe(grounding.Queries)
		grounding.Sources = uniqueSources(grounding.Sources)
		if len(grounding.Queries) > 0 || len(grounding.Sources) > 0 {
			result.Grounding = &grounding
			result.Warnings = append(result.Warnings, fmt.Sprintf("the model ran %d Google Search queries; any search grounding fee Google charges is not included in the cost", len(grounding.Queries)))
		} else {
			result.Warnings = append(result.Warnings, "googleSearch was on but the response carried no search details, so the model may not have searched; check facts in the image")
		}
	}
	result.Warnings = dedupe(result.Warnings)

	if succeeded == 0 {
		// No image, but the model may still have consumed billable tokens.
		return nil, s.settleUnusable(res, entry, firstErr, m, usage, billable, location, est)
	}
	// From here on Google has billed the call: always settle it as spent.
	entry.Status = spend.StatusOK
	entry.Usage = usage
	entry.Outputs = assetPaths(result.Files)
	if saveErr != nil {
		entry.Error = "saving output: " + saveErr.Error()
	}
	var actual *catalog.Estimate
	if a, ok := m.CostFromUsage(tokenUsage(billable), s.backend(), location); ok {
		actual = &a
	}
	result.Cost = s.settle(res, entry, est, actual)

	if len(result.Files) == 0 {
		return nil, fmt.Errorf("the image(s) were generated and billed (~$%.3f) but could not be saved: %w", result.Cost.USD, saveErr)
	}
	if saveErr != nil {
		result.Warnings = append(result.Warnings, "some images could not be saved: "+saveErr.Error())
	}
	if firstErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d of %d variations failed: %v", count-succeeded, count, firstErr))
	}
	return result, nil
}

// outputName returns the file name when ref points into the output directory.
func (s *Service) outputName(ref string) (string, bool) {
	if strings.HasPrefix(ref, store.URIScheme) {
		return strings.TrimPrefix(ref, store.URIScheme), true
	}
	if p, err := store.FilePath(ref); err == nil {
		ref = p
	}
	if !strings.ContainsAny(ref, `/\`) {
		return ref, true // bare name of an earlier output
	}
	abs, err := filepath.Abs(ref)
	if err != nil || filepath.Dir(abs) != s.store.Dir() {
		return "", false
	}
	return filepath.Base(abs), true
}

func imageParts(inputs []*store.Input, prompt string) []*genai.Part {
	parts := make([]*genai.Part, 0, len(inputs)+1)
	for _, in := range inputs {
		parts = append(parts, &genai.Part{InlineData: &genai.Blob{Data: in.Data, MIMEType: in.MIMEType}})
	}
	return append(parts, &genai.Part{Text: prompt})
}

func refsOf(inputs []*store.Input) []string {
	out := make([]string, len(inputs))
	for i, in := range inputs {
		out[i] = in.Ref
	}
	return out
}

func assetPaths(a []store.Asset) []string {
	out := make([]string, len(a))
	for i, x := range a {
		out[i] = x.Path
	}
	return out
}

func uniqueSources(xs []google.Source) []google.Source {
	seen := map[string]bool{}
	var out []google.Source
	for _, x := range xs {
		if !seen[x.URI] {
			seen[x.URI] = true
			out = append(out, x)
		}
	}
	return out
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
