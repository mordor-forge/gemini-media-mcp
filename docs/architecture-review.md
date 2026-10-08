# Architecture review and v1 refactor

*Reviewed 2026-09-28 against the Gemini API, Vertex AI (now "Gemini Enterprise Agent Platform"), MCP spec 2026-07-28, go-sdk v1.8.0 and go-genai v1.71.0. Updated 2026-10-07 for Nano Banana 2.1, Gemini Omni and go-genai v1.72.0, and 2026-10-08 for the Veo 3.1 previews leaving the Gemini API.*

## Verdict

The v0 code was small, idiomatic Go with clean package boundaries, typed tool inputs and decent unit tests. It was a good first MCP server. Its weakness was structural. Everything volatile about Google's media APIs was hardcoded in about six places:

- model IDs
- which parameters each model accepts
- prices
- lifecycle (preview, GA, shutdown dates)
- per-backend differences

Nine months of Google releases have broken it:

- The **default image models were shut down**.
- **Vertex AI video, images and music fail**.
- Two published prices were wrong.
- The TTS API changed semantics.

A cosmetic update would have fixed the IDs and broken again at the next launch.

**Recommendation, now implemented:** a structural refactor, not a rewrite. Go, the official MCP Go SDK and the official `google.golang.org/genai` SDK remain the right stack:

- A single static binary is the easiest artifact to distribute to every agent harness (release archives, MCPB, Docker, `go install`).
- The Go MCP SDK is Tier-1 and shipped MCP 2026-07-28 support on release day.

The internals were rebuilt around four ideas:

1. A **data-driven model catalog** that can be updated without a release.
2. **Explicit job handles** for long-running work.
3. An **append-only spend ledger** with budgets.
4. A **media store** that handles provenance and safe inputs.

On top of that sits a smaller, workflow-shaped tool surface that works over both stdio and Streamable HTTP.

---

## 1. What was broken (as of 2026-09-28)

| # | Severity | Problem | Evidence |
|---|---|---|---|
| 1 | Critical | Default image models `gemini-3.1-flash-image-preview` / `gemini-3-pro-image-preview` were shut down (Gemini API 2026-06-25, Vertex 2026-07-17). Every `generate_image` / `edit_image` / `compose_images` call fails. | Gemini API deprecations page; Vertex release notes |
| 2 | Critical | On Vertex, the Veo `*-preview` IDs were retired on 2026-04-02; Vertex uses `veo-3.1-*-001`. All Vertex video calls fail. | Vertex Veo 3.1 model page |
| 3 | Critical | Vertex location was forced to `us-central1` for every model. Nano Banana, Lyria 3 and TTS are served from `global` (or `us`/`eu`), so those calls fail on Vertex. | Vertex model pages; go-genai defaults to `global` |
| 4 | Critical | Video extension on Vertex was broken: the model was parsed from the operation name assuming the Gemini API format `models/X/operations/Y`, but Vertex names are `projects/…/publishers/google/models/X/operations/Y`. | `modelFromOperationName` in v0 |
| 5 | High | Authentication paths were ignored or misrouted: `GOOGLE_GENAI_USE_VERTEXAI`, the new `GOOGLE_GENAI_USE_ENTERPRISE`, `GOOGLE_CLOUD_REGION`, and Vertex express-mode API keys (sent to the Gemini API instead). There was no way to diagnose which path was active beyond `get_config`. | go-genai `client.go` env handling |
| 6 | High | Image parsing took the first inline blob, which can be an interim "thought" draft on Gemini 3 image models. Safety blocks surfaced as "no image data found", discarding the finish or block reason. | `extractFirstNonEmptyInlineData` in v0 |
| 7 | High | TTS files were saved as raw `.pcm`. The skill told users to play them with `ffplay -f s16le …`. Only 3 of 30 voices were documented. Gemini 3.8 TTS (2026-09-23) reads text verbatim, so "Say cheerfully: …" style prompts are spoken aloud. | Gemini 3.8 TTS guide |
| 8 | High | Prices were static strings, and several were wrong: Veo 3.1 Fast is $0.10/$0.12/$0.30 per second, not $0.15/$0.35; Lyria clip/pro are $0.04/$0.08 per request. There was no spend tracking of any kind. | Gemini API pricing (updated 2026-09-24) |
| 9 | High | Input images were read from any path with the MIME type guessed from the extension (unknown extensions became `image/png`). Acceptable over stdio; unsafe for an HTTP server. | `os.ReadFile(req.ImagePath)` in v0 |
| 10 | Medium | Video lifecycle took three tools (`video_status` → `download_video`) and the skill told the agent to "poll every 10–15 s", which agents can't do. Each poll cost a turn; there were no progress notifications. | v0 tools |
| 11 | Medium | Errors were not actionable (`generate image: generating image: Error 400 …`). There were no retries, no per-request timeouts, and no HTTP transport. The version was hardcoded to `0.1.0`. | v0 server |
| 12 | Medium | Skills were stale: dead model IDs and prices, a broken `references/prompt-guide.md` link, Claude-Code-only instructions ("use the Read tool"), always-interactive menus, and a TTS `model` parameter that did not exist. Packaging was `cp -r skills/x ~/.claude/skills/`. | v0 skills |
| 13 | Low | Dependencies were far behind. go-sdk was at v1.4.1 (it supports spec 2025-11-25; v1.7+ supports 2026-07-28). genai was at v1.52.1 against a current v1.71.0 (retries, Enterprise backend, `modelStatus`, webhooks). | Go proxy |

### About the v0 "provider abstraction"

v0 defined five interfaces (`ImageGenerator`, `VideoGenerator`, …) with a single implementation that mirrored the Gemini API one-to-one. It looked like portability but protected nothing that actually changed:

- Google never changed "generate an image".
- What changed was IDs, parameters, prices, lifecycle and backend quirks, and all of those were hardcoded behind the interface.

The useful seams turned out to be:

- **Catalog.** What models exist, and what they accept and cost.
- **Family adapter.** How a family is called: generateContent+IMAGE, generateVideos, generateContent+speechConfig, generateContent+AUDIO, or an Interactions API call (Omni).
- **`google.API`.** A narrow interface over the SDK, which also makes the service testable with a fake.

---

## 2. The v1 architecture

```
cmd/gemini-media-mcp          CLI: serve (stdio|http) · doctor · configure · models · usage · version
internal/
  config     layered config (defaults < file < env < flags), auth/backend resolution with reasons
  catalog    embedded models.yaml + hot-reloaded override: aliases, lifecycle, per-backend IDs/locations,
             capabilities, constraint rules, prices, estimates, usage→cost reconciliation
  google     SDK adapter: per-location client pool, retries, error classification, response parsing,
             request-body patches for features the SDK has not released yet
  media      workflows: resolve → validate → estimate → reserve budget → call → save → reconcile → settle
  store      output files: collision-free names, atomic writes, provenance sidecars, previews, WAV,
             safe input loading (paths, gemini-media:// URIs, data: URIs; allowlist over HTTP)
  jobs       persisted opaque video job handles (survive restarts, backend-agnostic)
  spend      append-only JSONL ledger shared across processes, budgets, confirmation threshold
  server     MCP tools/resources/instructions, progress bridge, stdio + Streamable HTTP
  apperr     classified errors: kind + message + remediation hint
```

A request flows like this. An MCP tool call reaches `server`, which attaches progress. `media` then:

1. Asks `catalog.Resolve` for the model. This redirects retired models, falls back per backend, and picks the Vertex location.
2. Validates the parameters against the catalog rules.
3. Loads inputs through `store` under the input policy.
4. Asks `catalog` for an estimate and reserves it with `spend.Reserve`, which enforces budgets and the confirmation threshold.
5. Calls `google.API`.
6. Has `google.ParseResponse` extract media, text, usage and `modelStatus` (safety blocks are classified here).
7. Saves outputs with `store.Save` (including provenance).
8. Reconciles the cost from `usageMetadata` and settles the reservation in the ledger.
9. Returns structured output plus text, resource links and an inline preview.

### 2.1 Resilience to model, API and configuration churn

- **Catalog is the single source of truth.** `internal/catalog/models.yaml` holds, for each model: aliases, family, status, release and shutdown dates, replacement, per-backend ID (`vertexId`), Vertex locations, backend fallback, capabilities, constraint rules and prices. Tool schemas, `list_models`, validation, estimates and skills all read from it.
- **Updates without a release.** Point `GEMINI_MEDIA_CATALOG` at an override YAML. Entries merge by `id` (maps merge key-by-key, lists replace) and are hot-reloaded within 5 seconds. A broken override keeps the last good catalog and reports the error in `get_config` and `list_models`.
- **Retirement handling:**
  - Retired models auto-redirect to their replacement, with a warning.
  - Deprecated models warn with their shutdown date.
  - Models missing on a backend fall back, e.g. 3.8 TTS falls back to 2.5 TTS on Vertex, and Lyria 3.5 to Lyria 3 Pro.
  - Lifecycles can differ per backend (`backendShutdown`), and so can defaults (`backendDefaults`). Google shuts the Veo 3.1 previews down on the Gemini API on 2026-10-22 while the `-001` models stay on Gemini Enterprise Agent Platform (Vertex AI). The catalog records that date on the Gemini API only: Veo warns there until the date and falls back to Omni after it, Vertex keeps Veo, and the video default is Omni on the Gemini API and Veo Lite on Vertex. Nano Banana (2.5) uses the same field for its earlier Gemini API shutdown. A default set in the server config still applies on both backends.
  - Retirement dates reported by the API (`modelStatus`) are surfaced as warnings.
- **Unknown model IDs pass through.** The family is inferred from the ID (`veo-*`, `gemini-omni-*`, `lyria-*`, `*tts*`, `gemini-*image*`, `gemini-*banana*`), so a model launched tomorrow works by raw ID before the catalog knows it. It is unvalidated and unpriced, and says so.
- **Live discovery.** `list_models live:true` asks the API what the key can actually call. It flags catalog models that are unavailable, and lists media models the catalog doesn't know yet. `gemini-media-mcp doctor` runs the same check from a terminal.
- **Configuration.** Loading is layered: defaults < `config.yaml` < env < flags. Unknown keys warn instead of failing, which gives forward compatibility. The Google SDK env names, the old env names and new `GEMINI_MEDIA_*` names are all honored. `get_config` reports the source of every setting.
- **Transient failures.** SDK-level retries with backoff are enabled only for 429 and 503, the codes that mean "not processed". Generation calls are billed and not idempotent: after a 500, 504 or 408 the work may still finish server-side, so a retry could double-charge. Per-request timeouts are configurable.
- **Crash-free startup.** A broken override file falls back to the embedded catalog and is reported, instead of preventing startup. Missing credentials put the server in a degraded mode where `get_config`, `list_models` and `estimate_cost` still work and generation tools return setup instructions.

### 2.2 Authentication

`config.ResolveAuth` produces the backend, the auth mode and a human-readable reason:

- **Explicit choice wins.** `GEMINI_MEDIA_BACKEND` / `--backend` / config file take priority. Next come the SDK switches `GOOGLE_GENAI_USE_ENTERPRISE` and then `GOOGLE_GENAI_USE_VERTEXAI`.
- **API key beats a leaked `GOOGLE_CLOUD_PROJECT`.** This preserves v0 behaviour, and the reason text says so.
- **Vertex with a project** uses Application Default Credentials. **Vertex with only an API key** uses express mode; video is rejected there because express mode has no Veo long-running operations.
- **Locations are per model, from the catalog.** Veo uses `us-central1`; images, Lyria and TTS use `global`. An explicit location is honored where the model is offered and overridden with a warning where it is not. One genai client is pooled per location.
- **Key migration warning.** `doctor` warns about standard `AIza…` Gemini API keys, which Google is migrating to `AQ.…` authorization keys. `configure --api-key-stdin` writes a `0600` config file for harnesses that don't forward environment variables (Gemini CLI redacts `*KEY*` variables; Agent Plugins forbids secrets in `env`).

### 2.3 Tool surface: 12 workflow-shaped tools

| v0 (12 tools) | v1 (12 tools) | Why |
|---|---|---|
| `generate_image`, `compose_images` | `generate_image` (+ `referenceImages` up to 14, `count` 1–4, `googleSearch`, `imageSize` 1K–4K, 512 only on Nano Banana 2) | Composition is generation with references. Fewer overlapping tools improve selection accuracy (RAG-MCP, arXiv:2505.03275). |
| `edit_image` | `edit_image` (+ references, aspect ratio / outpainting, size; defaults to the model that made the source) | Consistent quality across edit chains. |
| `generate_video`, `animate_image` | `generate_video` (+ `image`, `lastFrame`, `referenceImages`, `negativePrompt`, `seed`, `personGeneration`, `generateAudio`, `waitSeconds`) | Image-to-video, first+last-frame interpolation and "ingredients" are all Veo parameters. |
| `video_status`, `download_video` | `get_video` (long-poll up to `waitSeconds`, auto-download, idempotent) | One call per wait instead of several, with progress notifications. |
| `extend_video` | `extend_video` (by job handle; Veo on both backends, Omni on the Gemini API) | — |
| — | `edit_video` (Gemini Omni: a finished clip by `jobId`, in conversation, or a video file of up to 10 s) | Instruction-based video editing, new with Omni. |
| `generate_audio` | `generate_speech` (single voice or a 2-speaker `dialogue`, `style`, 30 voices, WAV) | "Audio" was ambiguous next to music. 3.8 TTS style is handled correctly. |
| `generate_music` | `generate_music` (+ `lyrics`, `instrumental`, `bpm`, `durationSeconds`, inspiration `images`, WAV on Lyria 3.5) | Structured hints are folded into the prompt, since Lyria has no dedicated fields. |
| `list_models` | `list_models` (`mediaType`, `detail`, `live`, `includeInactive`) | Discovery and availability checks. |
| `get_config` | `get_config` (backend reason, setting sources, warnings; never secrets) | Troubleshooting. |
| — | `estimate_cost`, `get_usage` | Spend awareness. |

Tool design follows Anthropic's [Writing effective tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents) and the findings of *MCP Tool Descriptions Are Smelly!* (arXiv:2602.14878: precise, purpose-first descriptions help; long ones add steps):

- Descriptions state purpose, when to use the tool, the key limits and the typical cost, and are capped at 450 characters by a test.
- Every tool has a title, annotations (`destructiveHint: false` for generators, `readOnlyHint` for info tools), a typed input schema with per-field guidance, and a structured output schema.
- Results carry:
  - a concise text summary with path, URI, size and cost;
  - `resource_link`s to `gemini-media://files/…`, which are readable via `resources/read` for remote clients;
  - an inline downscaled JPEG preview for images, so multimodal agents can check the result without filesystem access (about 800 tokens per preview; switch off with `inlinePreviews: false`).
- Errors are `isError` results formatted `[kind] message\nHint: next step`. Kinds: auth, permission, quota, not_found, invalid, safety, unavailable, timeout, budget, confirmation.
- Server `instructions` give the three rules that matter: chain outputs by URI, video is asynchronous, and mind the cost.

### 2.4 Long-running video: handles, not MCP Tasks (yet)

- **Why not Tasks.** MCP 2026-07-28 moved Tasks out of core into the `io.modelcontextprotocol/tasks` extension. go-sdk v1.8.0 does not implement it, and the official client matrix lists no client support.
- **What v1 does instead.** It follows the spec's own guidance for stateful tools: opaque, high-entropy handles passed as ordinary arguments.
  - `generate_video` persists a `job_…` handle in the state directory; it survives restarts and works across processes.
  - `get_video` long-polls with 5 s to 15 s backoff and emits progress notifications. Claude Code resets its idle timeout on progress and auto-backgrounds calls longer than 2 minutes.
  - The default wait is 45 s, below Codex's 60 s default tool timeout.
- **Future-proofing.** Job states (`working`, `completed`, `failed`, `filtered`) match the Tasks vocabulary, so a Tasks adapter can be added when SDKs and clients catch up.
- **Compatibility.** v0 operation names are still accepted.

### 2.5 Transports and security

- **stdio (default).** Logs go to stderr only.
- **Streamable HTTP (`--transport http`).**
  - Stateless by default: MCP 2026-07-28 removed sessions, go-sdk only accepts 2026-07-28 requests in stateless mode, and legacy clients still work.
  - Bearer token via `GEMINI_MEDIA_HTTP_TOKEN`, compared in constant time.
  - The server **refuses to bind a non-loopback address without a token**.
  - DNS-rebinding (Host) protection and cross-origin (`Sec-Fetch-Site`/`Origin`) protection are on; extra trusted origins are configurable.
  - `/healthz`, a 32 MiB request cap (to allow `data:` URI inputs), graceful shutdown, and request cancellation propagated to handlers.
- **Input files.**
  - Accepted forms: paths, `gemini-media://` URIs, bare output names and `data:` URIs.
  - Remote URLs are rejected, to prevent SSRF.
  - Over HTTP, reads are confined to the output directory plus configured `inputDirs`, after resolving symlinks so a symlink escape is blocked.
  - There is a 20 MB per-input limit, and MIME types are sniffed from content.
- **Outputs.**
  - Never overwrite an existing file: names are claimed with `O_EXCL`.
  - Written atomically (temp file plus rename).
  - A provenance sidecar (`.meta/<file>.json`) records tool, model, prompt, parameters, inputs, cost and model commentary.

### 2.6 Spend tracking

Google returns no cost with any response, and Veo operations carry no usage at all. The authoritative figures live in AI Studio and the Cloud Billing export, which lags by hours. The server therefore computes its own figures and labels them as estimates:

1. **Prices.** They live in the catalog (`pricing:` per model):
   - per-modality token rates;
   - image tokens per output size (NB2 747/1120/1680/2520 for 512/1K/2K/4K; NB 2.1 1120/1680/3780 for 1K/2K/4K, measured from live billing) and the typical thinking tokens per image (`textOutputTokens`);
   - Veo per-second rates by resolution, with and without audio (silent is Vertex-only);
   - Lyria per-request prices;
   - TTS audio tokens per second.

   Each entry carries `asOf` and `source`, and prices can be overridden without a release.
2. **Estimate, then reserve.** Before any API call, the estimate is reserved against session, daily and monthly caps. The reservation is itself a ledger line, and the check plus the reservation run under an exclusive lock on `usage.jsonl.lock` (flock / LockFileEx), so caps hold across concurrent calls and across several server processes sharing one state directory. The call's final entry supersedes its reservation. A process that dies mid-call leaves a reservation that expires after an hour (longer when request timeouts × retries could exceed it). Writers also terminate a torn last line left by an interrupted write before appending, and read every entry back.
3. **Confirmation threshold.** Calls estimated above `confirmAboveUsd` are refused with a `[confirmation]` error until retried with `approvedCostUsd`. That makes "ask the human" explicit and portable.

   Models without price data (a raw ID the catalog doesn't know yet) would estimate $0 and slip past every cap. When any budget or threshold is configured, they require `approvedCostUsd`, which is then reserved and recorded as their cost.

   Elicitation was not used because:
   - client support is uneven;
   - go-sdk's `Elicit` errors on 2026-07-28 sessions;
   - the MRTR alternative needs client support too.
4. **Reconcile.** After the call:
   - Token-priced models are priced from `usageMetadata` per modality. For images this matches Google's per-image prices exactly (Nano Banana 2.1 at 1K: 1120 tokens × $30/M = $0.034).
   - Veo is priced from parameters. Omni is priced from the video tokens the API reports (5,792 per second at 720p).
   - Failed and safety-filtered generations are recorded at $0.
   - A response that consumed tokens but returned no media (for example finish reason `MAX_TOKENS`) is recorded as spent at its token cost. Safety blocks keep their usage on the entry but stay at $0.
   - Outputs that Google generated (and billed) but the server could not save or download are recorded as spent, with the error attached. A video job in that state keeps reporting its cost.
   - Pending video jobs count toward caps until they settle.
5. **Report.** Results show `cost {estimatedUsd, usd, basis}`. `get_usage` breaks spend down by period, model and tool, with budget remaining and running jobs. `gemini-media-mcp usage` shows the same from a terminal. The ledger is plain JSONL, easy to import anywhere.

   Also set an AI Studio project spend cap or a Cloud budget as a hard backstop: the server's figures are estimates.

### 2.7 New Google features covered

| Area | Now supported |
|---|---|
| Images | GA Nano Banana 2.1 (default since 2026-10-06; Nano Banana 2 shuts down 2026-10-29) / Pro / 2 Lite; 1K–4K (512px on NB2 only); 14 aspect ratios (NB2/2.1); up to 14 references; Google Search grounding; 1–4 parallel variations; model commentary returned; thought images filtered |
| Video | Veo 3.1 Lite/Fast/Standard with backend-specific IDs (Gemini API until 2026-10-22, then Vertex only; Veo Lite is the Vertex default); first frame, first+last frame, reference "ingredients"; negative prompt; seed and silent video on Vertex; person generation; extension on both backends (720p sources, checked locally); MP4 duration read from the file; `raiMediaFilteredReasons` surfaced. Gemini Omni 1.1 Flash (Gemini API, its video default since 2026-10-08) through the Interactions API: 3–10 s at 360p–4K, first and last frames, up to 10 references, instruction-based editing (`edit_video`) and extension to 40 s, run as background jobs |
| Speech | Gemini 3.8 Flash / Flash-Lite TTS (verbatim text plus per-turn `speech_metadata` style, injected through the SDK's request hook until the SDK ships the field); legacy 2.5/3.1 models get in-text directions automatically; 2-speaker dialogue; all 30 voices; custom `voice_…` IDs; WAV output from both PCM and WAV responses |
| Music | Lyria 3.5 (full songs, WAV, image inspiration), Lyria 3 Clip / Pro; lyrics and structure returned |

### 2.8 Packaging for agent harnesses

One repository serves every harness from a single canonical `skills/` directory. There are no symlinks and no duplicated skill content. Plugin manifests launch `gemini-media-mcp` from `PATH`; users install the binary once from the release archives or with `go install`. The Gemini CLI release archives and the Claude Desktop bundle ship the binary themselves, and the Docker image needs no install. There is no npm package: it would mean seven packages and a publishing token to maintain.

| Harness | Manifest |
|---|---|
| Codex, VS Code/Copilot, Cursor | `plugin.json` + `mcp.json` (Agent Plugins 1.0) and a `.codex-plugin/` overlay that forwards credential environment variables |
| Claude Code, claude.ai, Cowork | `.claude-plugin/plugin.json` (with a keychain-stored `userConfig` API key), plus `.claude-plugin/marketplace.json` (also read by Codex, VS Code and `npx skills`) |
| Gemini CLI | `gemini-extension.json` with `settings`, since the CLI strips `*KEY*` variables. Release archives are named for the CLI's asset matcher |
| Claude Desktop | `mcpb/` bundle (manifest 0.3, universal macOS binary, Linux architecture launcher) |
| MCP Registry | `server.json` (schema 2025-12-11) with OCI and MCPB packages; published through GitHub OIDC |
| Docker | distroless non-root image on GHCR, `/output` and `/state` volumes, carrying the registry label |

A release is one tag. `scripts/sync-version.sh` stamps the version into every manifest, and `scripts/validate-packaging.sh` (also run in CI) checks versions, schemas and layout, calling `claude plugin validate`, `mcpb validate` and `mcp-publisher validate` when those tools are installed. `release.yml` then runs GoReleaser, the MCPB bundle, the GHCR image and the registry publish. Per-client install snippets are in [`packaging/INSTALL-SNIPPETS.md`](../packaging/INSTALL-SNIPPETS.md).

---

## 3. What I deliberately did not do (follow-ups)

1. **Gemini Omni Flash on Vertex AI.** Omni (`gemini-omni-1.1-flash`, `family: omni`) is supported on the Gemini API since go-genai v1.72.0 shipped an Interactions client. Each interaction is synchronous and takes minutes, so the server runs it in a background goroutine tracked as a job: the running session refreshes the job record, and a record whose session stopped is reported as interrupted (and possibly billed). Vertex AI serves it as `gemini-omni-1.1-flash-preview` through a preview Interactions API that the SDK does not yet support there. Omni's background mode and Files API uploads (for videos over 20 MB) are not used yet.
2. **Multi-turn conversational image editing.** This means re-sending prior turns with thought signatures. Single-turn edits with the source image are robust today; conversational mode would improve consistency over long edit chains.
3. **Extended Voice Library listing and voice design/replication.** `voice_…` IDs are passed through, but there is no listing or creation tool.
4. **Batch API** for bulk images and TTS (50% off). It fits a future `batch: true` flag with a job handle.
5. **Veo webhooks** instead of polling. They need a publicly reachable endpoint; revisit for hosted HTTP deployments.
6. **Lyria RealTime** (WebSocket streaming). It does not fit request/response tools.
7. **MCP Apps** (a `ui://` gallery and player) and the Skills-over-MCP extension, so skills can be served by the server itself.
8. **OAuth and multi-tenant hosting.** Per-user job and budget isolation, and the RFC 9728 metadata endpoint. The static bearer token covers self-hosting today.
9. **An OpenTelemetry exporter.** The ledger fields map onto `gen_ai.*` semantic conventions and can be exported later.
10. **An Antigravity CLI plugin.** Antigravity CLI replaced Gemini CLI for consumer accounts in June 2026. Its plugins are a folder with `plugin.json`, `mcp_config.json` and `skills/`, but its `plugin.json` schema reportedly accepts only `name` and `description`, which conflicts with the Agent Plugins 1.0 manifest at the repository root. Until that is tested with `agy`, the install snippets document a manual `mcp_config.json` entry.
11. **Live verification on Vertex AI.** The Gemini API has been tested live: the E2E suite (2026-09-29), a manual QA round of every tool (2026-10-06, which corrected Nano Banana 2.1's token counts and several naming and reporting issues) and the Omni clip-and-edit test (2026-10-07). Vertex AI has not been run live yet. Offline coverage:
    - unit and integration tests against an in-memory fake of the Google API;
    - tests that drive the real genai SDK against local fake HTTP servers, checking serialization, the TTS body patch, retries and the Interactions API;
    - end-to-end MCP tests over in-memory and HTTP transports;
    - stdio and HTTP smoke tests of the built binary.

    Live tests run locally only: `GEMINI_MEDIA_E2E=1 GEMINI_API_KEY=… go test -tags=e2e ./internal/media/ -run E2E -v -timeout 30m`, or with Vertex credentials (`GOOGLE_CLOUD_PROJECT` + ADC).

---

## 4. Migration from v0

| v0 | v1 |
|---|---|
| `compose_images {referenceImages}` | `generate_image {referenceImages}` |
| `animate_image {imagePath}` | `generate_video {image}` |
| `video_status` + `download_video {operationId}` | `get_video {jobId}` (old operation names are still accepted) |
| `extend_video {operationId}` | `extend_video {jobId}` |
| `generate_audio {prompt, voiceName, languageCode}` | `generate_speech {text, voice, languageCode, style}` |
| `edit_image {imagePath}` | `edit_image {image}` (path, URI or data URI) |
| `generate_image {resolution}` | `generate_image {imageSize}` |
| `generate_video {duration}` | `generate_video {durationSeconds}` |
| TTS output `.pcm` | `.wav` (`format: pcm` still available) |
| Aliases `nb2 pro lite fast standard tts clip full` | Unchanged, pointing at current models |
| Env `GOOGLE_API_KEY`, `GEMINI_API_KEY`, `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION`, `MEDIA_OUTPUT_DIR` | Unchanged; plus `GOOGLE_GENAI_USE_VERTEXAI` and `GEMINI_MEDIA_*` (see README) |
| Skills `gemini-image-gen`, `video-gen`, `music-gen`, `tts-gen` | `gemini-image`, `gemini-video`, `gemini-music`, `gemini-speech`, and a new `gemini-media-production` |

---

## Sources

Model IDs, lifecycle and prices:
- https://ai.google.dev/gemini-api/docs/models
- https://ai.google.dev/gemini-api/docs/deprecations
- https://ai.google.dev/gemini-api/docs/changelog
- https://ai.google.dev/gemini-api/docs/pricing (updated 2026-09-24)
- https://docs.cloud.google.com/gemini-enterprise-agent-platform/models/google-models
- https://cloud.google.com/gemini-enterprise-agent-platform/generative-ai/pricing

Per-family guides:
- https://ai.google.dev/gemini-api/docs/generate-content/image-generation
- https://ai.google.dev/gemini-api/docs/veo
- https://ai.google.dev/gemini-api/docs/generate-content/speech-generation
- https://ai.google.dev/gemini-api/docs/generate-content/music-generation
- https://ai.google.dev/gemini-api/docs/interactions-overview
- https://ai.google.dev/gemini-api/docs/api-key

MCP:
- https://github.com/modelcontextprotocol/modelcontextprotocol (spec 2026-07-28 changelog)
- https://github.com/modelcontextprotocol/ext-tasks
- https://github.com/modelcontextprotocol/go-sdk (v1.8.0 docs)

Google SDK source:
- https://github.com/googleapis/go-genai (v1.71.0 `client.go`, `types.go`, CHANGELOG)

Tool-design and packaging research:
- https://www.anthropic.com/engineering/writing-tools-for-agents
- arXiv:2602.14878 (MCP tool description smells)
- arXiv:2505.03275 (RAG-MCP: tool overload)
- Agent Skills specification: https://agentskills.io/specification
- Agent Plugins 1.0: https://github.com/agentplugins/agent-plugins-spec
- MCP Registry `server.json` schema 2025-12-11
