package catalog

import (
	"fmt"
	"math"
	"strings"
)

// Cost basis values (mirrors spend package constants).
const (
	BasisUnits    = "unit_params"
	BasisTokens   = "token_estimate"
	BasisUsage    = "usage_metadata"
	BasisUnpriced = "unpriced"
)

// defaultTextOutputTokens is the text/thinking output assumed per image
// request when the catalog gives no textOutputTokens (the model often adds a
// short caption).
const defaultTextOutputTokens = 400

func (p Pricing) textOutputTokens() int {
	if p.TextOutputTokens > 0 {
		return p.TextOutputTokens
	}
	return defaultTextOutputTokens
}

// Estimate is a cost figure with an explanation.
type Estimate struct {
	USD       float64 `json:"usd"`
	Basis     string  `json:"basis"`
	Breakdown string  `json:"breakdown"`
	PriceAsOf string  `json:"priceAsOf,omitempty"`
}

// TokenUsage is the backend-neutral token usage used for reconciliation.
type TokenUsage struct {
	PromptTokens     int
	OutputTokens     int
	ThoughtsTokens   int
	PromptByModality map[string]int
	OutputByModality map[string]int
}

func unpriced(m *Model) Estimate {
	return Estimate{Basis: BasisUnpriced, Breakdown: fmt.Sprintf("no price data for %s", m.ID)}
}

func (m *Model) multiplier(backend, location string) float64 {
	if backend == "vertex" && location != "" && location != "global" && m.Pricing.VertexRegionalMultiplier > 0 {
		return m.Pricing.VertexRegionalMultiplier
	}
	return 1
}

// DefaultImageSize returns the size used when a request omits imageSize.
func (m *Model) DefaultImageSize() string {
	if _, ok := m.Pricing.ImageOutputTokens["1K"]; ok || len(m.Capabilities.ImageSizes) == 0 {
		return "1K"
	}
	return m.Capabilities.ImageSizes[0]
}

// EstimateImage estimates count images of imageSize from a prompt of
// promptChars characters with inputImages reference/source images.
func (m *Model) EstimateImage(imageSize string, count, promptChars, inputImages int, backend, location string) Estimate {
	p := m.Pricing
	if len(p.OutputPer1M) == 0 {
		return unpriced(m)
	}
	if imageSize == "" {
		imageSize = m.DefaultImageSize()
	}
	count = max(count, 1)
	outTok, ok := p.ImageOutputTokens[imageSize]
	if !ok {
		outTok = maxTokens(p.ImageOutputTokens)
	}
	textTok := p.textOutputTokens()
	imageUSD := float64(outTok) * p.OutputPer1M["image"] / 1e6
	textUSD := float64(textTok) * p.OutputPer1M["text"] / 1e6
	input := float64(promptChars/4)*p.InputPer1M["text"]/1e6 + float64(inputImages*p.InputImageTokens)*rate(p.InputPer1M, "image", "text")/1e6
	mult := m.multiplier(backend, location)
	total := (imageUSD + textUSD + input) * float64(count) * mult
	return Estimate{
		USD:   round4(total),
		Basis: BasisTokens,
		Breakdown: fmt.Sprintf("%d x %s image: %d image tokens @ $%.2f/M ($%.4f) + ~%d text/thinking tokens @ $%.2f/M ($%.4f) + input ($%.4f%s)%s",
			count, imageSize, outTok, p.OutputPer1M["image"], imageUSD, textTok, p.OutputPer1M["text"], textUSD, input, inputNote(inputImages), multNote(mult)),
		PriceAsOf: p.AsOf,
	}
}

// EstimateVideo estimates count clips of seconds at resolution.
func (m *Model) EstimateVideo(resolution string, seconds, count int, audio bool, backend string) Estimate {
	p := m.Pricing
	if len(p.PerSecond) == 0 {
		return unpriced(m)
	}
	if resolution == "" {
		resolution = "720p"
	}
	if seconds <= 0 {
		seconds = max(m.Capabilities.DefaultDuration, 8)
	}
	count = max(count, 1)
	table, label := p.PerSecond, "with audio"
	if !audio && backend == "vertex" && len(p.PerSecondNoAudio) > 0 {
		table, label = p.PerSecondNoAudio, "video only"
	}
	rateUSD, ok := table[resolution]
	if !ok {
		rateUSD = maxRate(table)
	}
	return Estimate{
		USD:       round4(rateUSD * float64(seconds*count)),
		Basis:     BasisUnits,
		Breakdown: fmt.Sprintf("%d x %ds @ %s = $%.2f/s (%s)", count, seconds, resolution, rateUSD, label),
		PriceAsOf: p.AsOf,
	}
}

// SpeechSeconds estimates narration length from text (about 150 words/min).
func SpeechSeconds(text string) float64 {
	words := len(strings.Fields(text))
	return math.Max(1, float64(words)/2.5)
}

// EstimateSpeech estimates TTS cost for text.
func (m *Model) EstimateSpeech(text string) Estimate {
	p := m.Pricing
	if len(p.OutputPer1M) == 0 {
		return unpriced(m)
	}
	tps := p.AudioTokensPerSecond
	if tps == 0 {
		tps = 25
	}
	secs := SpeechSeconds(text)
	outTok := secs * float64(tps)
	usd := outTok*p.OutputPer1M["audio"]/1e6 + float64(len(text)/4)*p.InputPer1M["text"]/1e6
	note := ""
	if p.FreeTier {
		note = "; free tier may apply"
	}
	return Estimate{
		USD:       round4(usd),
		Basis:     BasisTokens,
		Breakdown: fmt.Sprintf("~%.0fs of audio (%.0f tokens @ $%.2f/M)%s", secs, outTok, p.OutputPer1M["audio"], note),
		PriceAsOf: p.AsOf,
	}
}

// EstimateMusic estimates count music generations.
func (m *Model) EstimateMusic(count int) Estimate {
	p := m.Pricing
	if p.PerRequest == 0 {
		return unpriced(m)
	}
	count = max(count, 1)
	return Estimate{
		USD:       round4(p.PerRequest * float64(count)),
		Basis:     BasisUnits,
		Breakdown: fmt.Sprintf("%d x $%.2f per song", count, p.PerRequest),
		PriceAsOf: p.AsOf,
	}
}

// CostFromUsage prices reported token usage. ok=false when the model is not
// token-priced or no usage was reported.
func (m *Model) CostFromUsage(u TokenUsage, backend, location string) (Estimate, bool) {
	p := m.Pricing
	if len(p.OutputPer1M) == 0 || (u.PromptTokens == 0 && u.OutputTokens == 0) {
		return Estimate{}, false
	}
	var usd float64
	var parts []string
	// Input: per modality when reported, else all at the text rate.
	if len(u.PromptByModality) > 0 {
		for mod, n := range u.PromptByModality {
			usd += float64(n) * rate(p.InputPer1M, mod, "text") / 1e6
		}
	} else {
		usd += float64(u.PromptTokens) * p.InputPer1M["text"] / 1e6
	}
	parts = append(parts, fmt.Sprintf("%d input tokens", u.PromptTokens))

	imageTok := u.OutputByModality["image"]
	audioTok := u.OutputByModality["audio"]
	videoTok := u.OutputByModality["video"]
	textTok := max(u.OutputTokens-imageTok-audioTok-videoTok, 0) + u.ThoughtsTokens
	if imageTok == 0 && audioTok == 0 && videoTok == 0 && len(u.OutputByModality) == 0 {
		// No modality breakdown: attribute output to the model's primary modality.
		switch m.MediaType {
		case Image:
			imageTok, textTok = u.OutputTokens, u.ThoughtsTokens
		case Speech:
			audioTok, textTok = u.OutputTokens, u.ThoughtsTokens
		case Video:
			videoTok, textTok = u.OutputTokens, u.ThoughtsTokens
		}
	}
	usd += float64(imageTok) * p.OutputPer1M["image"] / 1e6
	usd += float64(audioTok) * p.OutputPer1M["audio"] / 1e6
	usd += float64(videoTok) * p.OutputPer1M["video"] / 1e6
	usd += float64(textTok) * rate(p.OutputPer1M, "text", "") / 1e6
	if imageTok > 0 {
		parts = append(parts, fmt.Sprintf("%d image tokens", imageTok))
	}
	if audioTok > 0 {
		parts = append(parts, fmt.Sprintf("%d audio tokens", audioTok))
	}
	if videoTok > 0 {
		parts = append(parts, fmt.Sprintf("%d video tokens", videoTok))
	}
	if textTok > 0 {
		parts = append(parts, fmt.Sprintf("%d text/thinking tokens", textTok))
	}
	mult := m.multiplier(backend, location)
	return Estimate{
		USD:       round4(usd * mult),
		Basis:     BasisUsage,
		Breakdown: strings.Join(parts, ", ") + multNote(mult),
		PriceAsOf: p.AsOf,
	}, true
}

// PriceSummary renders a one-line human summary of the price table.
func (m *Model) PriceSummary() string {
	p := m.Pricing
	switch {
	case len(p.PerSecond) > 0:
		return "per second: " + fmtRates(p.PerSecond, []string{"360p", "720p", "1080p", "4k"})
	case len(p.ImageOutputTokens) > 0 && p.OutputPer1M["image"] > 0:
		var parts []string
		for _, size := range []string{"512", "1K", "2K", "4K"} {
			if tok, ok := p.ImageOutputTokens[size]; ok {
				parts = append(parts, fmt.Sprintf("%s $%.3f", size, float64(tok)*p.OutputPer1M["image"]/1e6))
			}
		}
		return "per image: " + strings.Join(parts, ", ") + " (image tokens only; prompt and thinking tokens add a little, see estimate_cost)"
	case p.OutputPer1M["audio"] > 0:
		perMin := 60 * float64(max(p.AudioTokensPerSecond, 25)) * p.OutputPer1M["audio"] / 1e6
		s := fmt.Sprintf("~$%.3f per minute of audio ($%.2f/M audio tokens)", perMin, p.OutputPer1M["audio"])
		if p.FreeTier {
			s += "; free tier available"
		}
		return s
	case p.PerRequest > 0:
		return fmt.Sprintf("$%.2f per generation", p.PerRequest)
	}
	return "unknown"
}

func fmtRates(t map[string]float64, order []string) string {
	var parts []string
	for _, k := range order {
		if v, ok := t[k]; ok {
			parts = append(parts, fmt.Sprintf("%s $%.2f", k, v))
		}
	}
	return strings.Join(parts, ", ")
}

func rate(t map[string]float64, key, fallback string) float64 {
	if v, ok := t[key]; ok {
		return v
	}
	return t[fallback]
}

func maxTokens(t map[string]int) int {
	best := 0
	for _, v := range t {
		best = max(best, v)
	}
	return best
}

func maxRate(t map[string]float64) float64 {
	best := 0.0
	for _, v := range t {
		best = math.Max(best, v)
	}
	return best
}

func inputNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(", incl. %d input image(s)", n)
}

func multNote(mult float64) string {
	if mult == 1 {
		return ""
	}
	return fmt.Sprintf(" x%.2f regional", mult)
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
