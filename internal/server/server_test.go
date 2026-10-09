package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"

	"github.com/mordor-forge/gemini-media-mcp/internal/catalog"
	"github.com/mordor-forge/gemini-media-mcp/internal/config"
	"github.com/mordor-forge/gemini-media-mcp/internal/google/googletest"
	"github.com/mordor-forge/gemini-media-mcp/internal/jobs"
	"github.com/mordor-forge/gemini-media-mcp/internal/media"
	"github.com/mordor-forge/gemini-media-mcp/internal/spend"
	"github.com/mordor-forge/gemini-media-mcp/internal/store"
)

func newTestServer(t *testing.T) (*Server, *googletest.Fake) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.OutputDir = filepath.Join(dir, "out")
	cfg.StateDir = filepath.Join(dir, "state")
	st, err := store.New(cfg.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	led, _ := spend.Open(filepath.Join(cfg.StateDir, "usage.jsonl"), spend.Budget{})
	reg, _ := jobs.Open(filepath.Join(cfg.StateDir, "jobs"))
	src, _ := catalog.NewSource("", nil, nil)
	auth := &config.Auth{Backend: config.BackendGeminiAPI, Mode: config.AuthAPIKey, APIKey: "k", Reason: "test"}
	fake := &googletest.Fake{BackendName: auth.Backend}
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 32, 32)))
	img := buf.Bytes()
	fake.ContentFn = func(googletest.ContentCall) (*genai.GenerateContentResponse, error) {
		return googletest.ImageResponse(img), nil
	}
	svc := media.New(media.Deps{API: fake, Auth: auth, Config: cfg, Catalog: src, Store: st, Jobs: reg, Ledger: led})
	return New(svc, st, Options{Transport: "stdio"}), fake
}

func connect(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.MCP().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestToolsAreListedWithAnnotationsAndSchemas(t *testing.T) {
	s, _ := newTestServer(t)
	cs := connect(t, s)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"edit_image", "edit_video", "estimate_cost", "extend_video", "generate_image", "generate_music", "generate_speech", "generate_video", "get_config", "get_usage", "get_video", "list_models", "stitch_tiles", "tile_image"}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		if tool.Annotations == nil || tool.Title == "" || tool.Description == "" {
			t.Errorf("%s: missing title/annotations/description", tool.Name)
		}
		if len(tool.Description) > 450 {
			t.Errorf("%s: description too long (%d chars); keep tool descriptions tight", tool.Name, len(tool.Description))
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s: missing output schema", tool.Name)
		}
		if strings.HasPrefix(tool.Name, "generate_") && (tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint) {
			t.Errorf("%s: generative tools are non-destructive writes", tool.Name)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	// Required fields are marked in the input schema.
	for _, tool := range res.Tools {
		if tool.Name != "generate_image" {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		if !strings.Contains(string(raw), `"required":["prompt"]`) {
			t.Fatalf("generate_image schema should require only prompt: %s", raw)
		}
	}
	init := cs.InitializeResult()
	if init == nil || !strings.Contains(init.Instructions, "get_video") {
		t.Fatalf("server instructions missing: %+v", init)
	}
}

func TestGenerateImageToolResult(t *testing.T) {
	s, fake := newTestServer(t)
	cs := connect(t, s)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{"prompt": "a cat", "outputName": "cat"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %v", res.Content)
	}
	var link *mcp.ResourceLink
	var preview *mcp.ImageContent
	var text string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			text = v.Text
		case *mcp.ResourceLink:
			link = v
		case *mcp.ImageContent:
			preview = v
		}
	}
	if link == nil || link.URI != store.URIScheme+"cat.png" || preview == nil || preview.MIMEType != "image/jpeg" {
		t.Fatalf("content = %+v", res.Content)
	}
	if !strings.Contains(text, "cat.png") || !strings.Contains(text, "Cost:") {
		t.Fatalf("text = %q", text)
	}
	structured, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(structured), `"model":"gemini-nano-banana-2.1"`) || strings.Contains(string(structured), "Previews") {
		t.Fatalf("structured = %s", structured)
	}
	if len(fake.ContentCalls) != 1 {
		t.Fatal("expected one API call")
	}

	// The saved file is readable as an MCP resource (for remote clients).
	rr, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: link.URI})
	if err != nil || len(rr.Contents) != 1 || rr.Contents[0].MIMEType != "image/png" || len(rr.Contents[0].Blob) == 0 {
		t.Fatalf("resource read = %+v %v", rr, err)
	}
	if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: store.URIScheme + "missing.png"}); err == nil {
		t.Fatal("missing resource should error")
	}
}

func TestToolErrorsAreActionable(t *testing.T) {
	s, _ := newTestServer(t)
	cs := connect(t, s)
	// No video model offers 8K, so this stays invalid whichever model the
	// lifecycle dates pick.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "generate_video", Arguments: map[string]any{"prompt": "x", "resolution": "8k"}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !res.IsError || !strings.HasPrefix(text, "[invalid]") || !strings.Contains(text, "Hint:") {
		t.Fatalf("want actionable isError result, got %v %q", res.IsError, text)
	}
	// Schema validation failures are tool errors too (SDK >= 1.5).
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{}})
	if err != nil || !res.IsError {
		t.Fatalf("missing prompt should be an isError result: %v %+v", err, res)
	}
}

func TestVideoToolsRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)
	cs := connect(t, s)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "generate_video", Arguments: map[string]any{"prompt": "waves"}})
	if err != nil || res.IsError {
		t.Fatalf("generate_video: %v %+v", err, res)
	}
	var job media.VideoJob
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &job)
	if job.State != jobs.StateWorking || job.JobID == "" {
		t.Fatalf("job = %+v", job)
	}
	// The default video model on the Gemini API is Omni, which answers in
	// the background.
	if job.Model != "gemini-omni-1.1-flash" {
		t.Fatalf("default video model = %s", job.Model)
	}
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_video", Arguments: map[string]any{"jobId": job.JobID, "waitSeconds": 10}})
	if err != nil || res.IsError {
		t.Fatalf("get_video: %v %+v", err, res)
	}
	raw, _ = json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &job)
	if job.State != jobs.StateCompleted || len(job.Files) != 1 {
		t.Fatalf("completed job = %+v", job)
	}
}

func TestInfoTools(t *testing.T) {
	s, _ := newTestServer(t)
	cs := connect(t, s)
	for _, call := range []mcp.CallToolParams{
		{Name: "list_models", Arguments: map[string]any{"mediaType": "video"}},
		{Name: "estimate_cost", Arguments: map[string]any{"mediaType": "image", "imageSize": "4K", "compare": true}},
		{Name: "get_usage", Arguments: map[string]any{}},
		{Name: "get_config", Arguments: map[string]any{}},
	} {
		res, err := cs.CallTool(context.Background(), &call)
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", call.Name, err, res)
		}
		if res.StructuredContent == nil {
			t.Fatalf("%s: no structured content", call.Name)
		}
	}
	res, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_config", Arguments: map[string]any{}})
	raw, _ := json.Marshal(res.StructuredContent)
	if strings.Contains(string(raw), `"k"`) || strings.Contains(strings.ToLower(string(raw)), "apikey") {
		t.Fatalf("get_config must not leak credentials: %s", raw)
	}
}

func TestHTTPHandler(t *testing.T) {
	s, _ := newTestServer(t)
	h, err := s.HTTPHandler(config.HTTP{Path: "/mcp", AuthToken: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: %v %v", err, resp)
	}
	_ = resp.Body.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token must be rejected: %v %v", err, resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Cross-origin browser requests are rejected even with a token.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin request must be rejected: %v %v", err, resp.StatusCode)
	}
	_ = resp.Body.Close()

	// A real MCP client with the token works end to end.
	httpClient := &http.Client{Transport: bearer{"s3cret"}}
	c := mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 14 {
		t.Fatalf("tools over HTTP: %v %v", err, tools)
	}
	out, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{"prompt": "x"}})
	if err != nil || out.IsError {
		t.Fatalf("call over HTTP: %v %+v", err, out)
	}
}

// resources/read must not follow a symlink out of the output directory, over
// stdio or the authenticated HTTP endpoint (regression).
func TestResourceReadEnforcesContainment(t *testing.T) {
	s, _ := newTestServer(t)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(s.store.Dir(), "leak.png")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cs := connect(t, s)
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: store.URIScheme + "leak.png"})
	if err == nil {
		t.Fatalf("escaping symlink was served: %q", res.Contents[0].Blob)
	}
	if strings.Contains(err.Error(), "top secret") || strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks the target: %v", err)
	}

	// Generated files are still readable.
	out, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{"prompt": "x"}})
	if err != nil || out.IsError {
		t.Fatalf("generate: %v %+v", err, out)
	}
	var uri string
	for _, c := range out.Content {
		if l, ok := c.(*mcp.ResourceLink); ok {
			uri = l.URI
		}
	}
	if uri == "" {
		t.Fatal("no resource link in result")
	}
	if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri}); err != nil {
		t.Fatalf("reading a generated file: %v", err)
	}

	// A file over the limit is refused before it is read, with what to do.
	defer func(v int64) { maxResourceBytes = v }(maxResourceBytes)
	maxResourceBytes = 10
	_, err = cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err == nil || !strings.Contains(err.Error(), "more than resources/read sends") || !strings.Contains(err.Error(), "output directory") {
		t.Fatalf("oversized resource: %v", err)
	}
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// Over HTTP a resources/read holds its slot until its response is written,
// not just until the handler returns, so concurrent reads cannot pile up
// in memory; other requests are not held up, and the queue is bounded.
func TestResourceReadsAreGatedForTheirWholeRequest(t *testing.T) {
	s, _ := newTestServer(t)
	release := make(chan struct{})
	entered := make(chan string, 16)
	h := s.gateResourceReads(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		entered <- string(body)
		if readsResource(body) {
			<-release // the response is still being written
		}
		w.WriteHeader(http.StatusOK)
	}))
	post := func(body string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)))
		return rec.Code
	}
	read := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"gemini-media://files/x.png"}}`
	padded := strings.Repeat(" ", 100_000) + read

	codes := make(chan int, 16)
	go func() { codes <- post(read) }()
	<-entered
	go func() { codes <- post(padded) }()
	if code := post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); code != http.StatusOK {
		t.Fatalf("tools/list while a read is served: %d", code)
	}
	if b := <-entered; !strings.Contains(b, "tools/list") {
		t.Fatalf("a second read ran alongside the first: %.80q", b)
	}
	for i := 1; i < maxResourceWaiters; i++ {
		go func() { codes <- post(read) }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.resourceWaiting.Load() < maxResourceWaiters {
		if time.Now().After(deadline) {
			t.Fatalf("%d reads waiting, want %d", s.resourceWaiting.Load(), maxResourceWaiters)
		}
		time.Sleep(time.Millisecond)
	}
	if code := post(read); code != http.StatusServiceUnavailable {
		t.Fatalf("a read past the queue: %d, want 503", code)
	}
	close(release)
	for range 1 + maxResourceWaiters {
		if code := <-codes; code != http.StatusOK {
			t.Fatalf("queued read: %d", code)
		}
	}
	if s.resourceWaiting.Load() != 0 || len(s.resourceHTTP) != 0 {
		t.Fatal("the gate did not release its slot and waiters")
	}
}
