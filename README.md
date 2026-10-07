# gemini-media-mcp

[![CI](https://github.com/mordor-forge/gemini-media-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/mordor-forge/gemini-media-mcp/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

An MCP server for Google's generative media models: **images** (Nano Banana 2.1 / Pro), **video** (Veo 3.1 and Gemini Omni Flash), **speech** (Gemini 3.8 TTS) and **music** (Lyria 3.5). It ships as a single Go binary, speaks **stdio and Streamable HTTP** (MCP 2026-07-28), works with the **Gemini API or Vertex AI**, and comes with **agent skills** and plugin packaging for Claude Code, Codex, Gemini CLI, VS Code/Copilot, Cursor and more.

- **Current models, updated without a release.** A built-in catalog records IDs, aliases, lifecycle, parameters and prices. Retired models redirect to their replacement, and new model IDs work before the catalog knows them. You can override or extend the catalog with a hot-reloaded YAML file.
- **Cost-aware.** Every result reports its estimated cost, and `estimate_cost` compares options before you spend. Spend is recorded in a ledger, capped by session, daily and monthly budgets, and calls above a threshold need explicit approval.
- **Agent-friendly.** Each tool matches a workflow, and errors come back as `[kind] message + Hint`. Image results include inline previews, and outputs can be chained by URI. Video runs as async jobs with long-polling and progress notifications.
- **Robust.** Backend and credentials are detected the way the Google SDKs do it. Vertex locations are chosen per model, retries and timeouts are built in, files are written atomically with provenance, and HTTP mode ships with security defaults.

> Upgrading from v0? The tools changed. See the [migration table](docs/architecture-review.md#4-migration-from-v0). The review also covers what was broken, why, and the design of v1.

## Quick start

1. **Get credentials.** Either:
   - an API key from [Google AI Studio](https://aistudio.google.com/apikey) (`GEMINI_API_KEY`), or
   - a Google Cloud project with Vertex AI enabled (`GOOGLE_CLOUD_PROJECT` plus `gcloud auth application-default login`).
2. **Install the binary.** On macOS or Linux:
   ```bash
   mkdir -p ~/.local/bin   # must be on your PATH
   os=$(uname -s | tr '[:upper:]' '[:lower:]'); arch=$(uname -m | sed 's/x86_64/x64/; s/aarch64/arm64/')
   curl -fsSL "https://github.com/mordor-forge/gemini-media-mcp/releases/latest/download/$os.$arch.gemini-media-mcp.tar.gz" \
     | tar -xz -C ~/.local/bin gemini-media-mcp
   gemini-media-mcp version
   ```
   On Windows, download the `windows` zip from the [latest release](https://github.com/mordor-forge/gemini-media-mcp/releases/latest) and put `gemini-media-mcp.exe` on your PATH. With Go 1.26+: `go install github.com/mordor-forge/gemini-media-mcp/cmd/gemini-media-mcp@latest`.

   The Gemini CLI extension, the Claude Desktop bundle and the Docker image include the binary, so they skip this step.
3. **Add the server to your agent.** Every config below runs `gemini-media-mcp` from your PATH.

   **Claude Code** (plugin: server plus skills):
   ```
   /plugin marketplace add mordor-forge/gemini-media-mcp
   /plugin install gemini-media@mordor-forge
   ```
   or just the server: `claude mcp add gemini-media -e GEMINI_API_KEY=... -- gemini-media-mcp`

   **Codex** (`~/.codex/config.toml`):
   ```toml
   [mcp_servers.gemini-media]
   command = "gemini-media-mcp"
   env_vars = ["GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION"]
   tool_timeout_sec = 300   # 4K images and video polls can exceed the 60 s default
   ```

   **Gemini CLI:** `gemini extensions install https://github.com/mordor-forge/gemini-media-mcp`

   **Claude Desktop:** download `gemini-media-mcp-<version>.mcpb` from the [latest release](https://github.com/mordor-forge/gemini-media-mcp/releases/latest) and open it.

   **VS Code / Copilot:** `code --add-mcp '{"name":"gemini-media","command":"gemini-media-mcp"}'`

   **Cursor, Windsurf and other `mcpServers`-style clients** (Zed, OpenCode and Goose use their own formats, see below):
   ```json
   { "mcpServers": { "gemini-media": { "command": "gemini-media-mcp", "env": { "GEMINI_API_KEY": "..." } } } }
   ```

   If a desktop app reports that `gemini-media-mcp` was not found, it doesn't see your shell's PATH: use the full path from `command -v gemini-media-mcp`. Every client, plus Docker and the skills installer, is covered in [packaging/INSTALL-SNIPPETS.md](packaging/INSTALL-SNIPPETS.md).
4. **If your agent doesn't forward environment variables** (plugins often don't), store the key once:
   ```bash
   echo "$GEMINI_API_KEY" | gemini-media-mcp configure --api-key-stdin
   gemini-media-mcp doctor   # checks credentials, backend and model availability
   ```

To run it in Docker instead: `docker run -i --rm --user "$(id -u):$(id -g)" -e GEMINI_API_KEY -v "$PWD/media:/output" -v gemini-media-state:/state ghcr.io/mordor-forge/gemini-media-mcp`
(`--user` makes the generated files yours on Linux; the named `/state` volume keeps spend accounting and video jobs between runs).

## Tools

| Tool | What it does |
|---|---|
| `generate_image` | Text-to-image with up to 14 reference images, 1K–4K, many aspect ratios, 1–4 variations, optional Google Search grounding |
| `edit_image` | Change an existing image (add/remove/restyle/relight/outpaint) while keeping the rest |
| `generate_video` | Clip with native audio from text, a first frame, first+last frames, or reference images: Veo (4–8 s, up to 3 references) or Gemini Omni Flash (`omni`: 3–10 s, 360p drafts to 4K, up to 10 references). Returns a `jobId` |
| `get_video` | Wait for a job (long-poll, default 45 s); downloads the video when done. Safe to repeat |
| `extend_video` | Continue a finished clip: Omni adds up to 10 s (40 s total), Veo about 7 s (up to 148 s total) |
| `edit_video` | Change a finished clip or a video file of up to 10 s with an instruction (Gemini Omni) |
| `generate_speech` | Text-to-speech (WAV): one voice, or a two-speaker dialogue, with per-line style control and 30 voices |
| `generate_music` | 30-second clips or full songs with lyrics, structure tags, tempo, instrumental mode and image inspiration |
| `list_models` | Current models, aliases, status, prices; `detail` for supported parameters, `live` to check what your key can use |
| `estimate_cost` | Price a request before running it and compare models |
| `get_usage` | Estimated spend by period, model and tool; budgets remaining; running jobs |
| `get_config` | Active backend and why it was chosen, output directory, defaults, warnings |

Every result includes the saved file's path, a `gemini-media://files/<name>` URI and its cost. You can pass the URI as an input to another tool. Clients that can't read the server's disk (for example over HTTP) can fetch the file with `resources/read`.

## Models

Use an alias or a full model ID. Run `gemini-media-mcp models` for the built-in catalog with prices, or call `list_models`, which also applies your override file and, with `live: true`, checks what your key can use.

| Media | Aliases (default first) |
|---|---|
| Image | `nb2` (Nano Banana 2.1, default), `pro` (Nano Banana Pro: highest fidelity), `nb2-lite` (cheapest inputs) |
| Video | `lite` (cheapest Veo), `omni` (Gemini Omni Flash: prompt adherence, editing, extension to 40 s; Gemini API only), `fast` (Veo 4K, references, extension), `standard` (highest Veo quality) |
| Speech | `tts` (Gemini 3.8 Flash TTS), `tts-lite`, `tts-2.5`, `tts-pro` |
| Music | `clip` (30 s), `full` (Lyria 3.5 songs) |

**Updating or adding a model without waiting for a release.** Create a YAML file and point `GEMINI_MEDIA_CATALOG` at it. The server merges it with the built-in catalog by `id` and reloads it automatically:

```yaml
defaults:
  video: fast
models:
  - id: veo-3.1-fast-generate-preview
    pricing: { perSecond: { 720p: 0.10, 1080p: 0.12, 4k: 0.30 } }
  - id: veo-4.0-generate-preview        # a brand-new model
    aliases: [veo4]
    pricing: { perSecond: { 720p: 0.50 } }
```

See [internal/catalog/models.yaml](internal/catalog/models.yaml) for the full schema.

## Configuration

Settings are layered: built-in defaults < config file < environment < flags. The config file lives at `~/.config/gemini-media-mcp/config.yaml` on Linux, `~/Library/Application Support/gemini-media-mcp/config.yaml` on macOS, `%AppData%\gemini-media-mcp\config.yaml` on Windows, or wherever `GEMINI_MEDIA_CONFIG` points. Unknown keys produce warnings instead of errors, and `get_config` shows where each setting came from.

| Environment variable | Config key | Default | Purpose |
|---|---|---|---|
| `GEMINI_API_KEY` / `GOOGLE_API_KEY` / `GEMINI_MEDIA_API_KEY` | `apiKey` | – | Gemini API key (or Vertex express-mode key) |
| `GEMINI_MEDIA_PROJECT` / `GOOGLE_CLOUD_PROJECT` | `project` | – | Vertex AI project (Application Default Credentials) |
| `GEMINI_MEDIA_LOCATION` / `GOOGLE_CLOUD_LOCATION` / `GOOGLE_CLOUD_REGION` | `location` | per model | Vertex region; models not offered there use their catalog location |
| `GOOGLE_GENAI_USE_VERTEXAI` / `GOOGLE_GENAI_USE_ENTERPRISE` | – | – | `true` selects Vertex AI, `false` the Gemini API (same semantics as the Google SDKs) |
| `GEMINI_MEDIA_BACKEND` | `backend` | `auto` | `auto`, `gemini-api` or `vertex` |
| `MEDIA_OUTPUT_DIR` / `GEMINI_MEDIA_OUTPUT_DIR` | `outputDir` | `~/generated_media` | Where media is saved |
| `GEMINI_MEDIA_STATE_DIR` | `stateDir` | `$XDG_STATE_HOME/gemini-media-mcp`, else `~/.local/state/gemini-media-mcp` (Linux) or the config directory | Spend ledger and video jobs |
| `GEMINI_MEDIA_BUDGET_SESSION_USD` / `_DAILY_USD` / `_MONTHLY_USD` | `budget.*Usd` | none | Spend caps (estimated) |
| `GEMINI_MEDIA_CONFIRM_ABOVE_USD` | `budget.confirmAboveUsd` | none | Calls above this need `approvedCostUsd` |
| `GEMINI_MEDIA_IMAGE_MODEL` / `_VIDEO_MODEL` / `_SPEECH_MODEL` / `_MUSIC_MODEL` / `GEMINI_MEDIA_VOICE` | `defaults.*` | catalog | Default models and voice |
| `GEMINI_MEDIA_CATALOG` | `catalogFile` | – | Catalog override file (hot-reloaded) |
| `GEMINI_MEDIA_INPUT_DIRS` | `inputDirs` | – | Extra directories inputs may be read from (HTTP mode) |
| `GEMINI_MEDIA_ALLOW_ANY_INPUT_PATH` | `allowAnyInputPath` | `true` on stdio, `false` on HTTP | Read input files from anywhere on disk |
| `GEMINI_MEDIA_INLINE_PREVIEWS` / `GEMINI_MEDIA_PREVIEW_MAX_PIXELS` | `inlinePreviews` / `previewMaxPixels` | `true` / `768` | Attach a downscaled preview to image results, and its longest side |
| `GEMINI_MEDIA_REQUEST_TIMEOUT_SECONDS` | `requestTimeoutSeconds` | `600` | Timeout of each Google API request |
| `GEMINI_MEDIA_MAX_VIDEO_WAIT_SECONDS` | `maxVideoWaitSeconds` | `600` | Upper limit for `waitSeconds` on video tools |
| `GEMINI_MEDIA_RETRY_ATTEMPTS` | `retry.attempts` | `4` | Attempts for rate-limited (429) and unavailable (503) responses |
| `GEMINI_MEDIA_LOG_LEVEL` | `logLevel` | `info` | `debug`, `info`, `warn` or `error` (logs go to stderr) |
| `GEMINI_MEDIA_TRANSPORT` | `transport` | `stdio` | `stdio` or `http` |
| `GEMINI_MEDIA_HTTP_ADDR` / `GEMINI_MEDIA_HTTP_TOKEN` | `http.addr` / `http.authToken` | `127.0.0.1:8765` / – | HTTP listen address and bearer token |
| – | `http.path` | `/mcp` | HTTP endpoint path |
| `GEMINI_MEDIA_HTTP_ALLOWED_ORIGINS` | `http.allowedOrigins` | – | Extra trusted browser origins, comma-separated |
| `GEMINI_MEDIA_HTTP_STATEFUL` | `http.stateful` | `false` | Keep per-client sessions (only for legacy 2025-11-25 clients that need them) |

How the backend is chosen:
1. An explicit `backend` setting wins.
2. Otherwise the SDK switches `GOOGLE_GENAI_USE_ENTERPRISE` and `GOOGLE_GENAI_USE_VERTEXAI` decide.
3. Otherwise an API key selects the Gemini API, even if `GOOGLE_CLOUD_PROJECT` is set in your shell for other tools.
4. Otherwise a project selects Vertex AI.

On Vertex, an API key without a project uses express mode, which does not support video.

## Spend and budgets

Google doesn't return costs, so the server computes them from its price table and the token usage the API reports. Every call records an estimate before it runs and the reconciled cost afterwards. Failed and safety-blocked generations count as $0. A response that used tokens but returned no media (for example `MAX_TOKENS`) is charged for those tokens.

- Results include `cost {estimatedUsd, usd, basis}`. `get_usage` and `gemini-media-mcp usage` summarize spend. The ledger is a plain JSONL file in the state directory.
- Budgets are enforced before each call, across concurrent calls and across several server processes sharing a state directory (in-flight calls hold their estimate under a file lock).
- With `GEMINI_MEDIA_CONFIRM_ABOVE_USD=1`, an expensive call (for example a $3.20 Veo clip) comes back as a `[confirmation]` error. The agent asks you, then retries with `approvedCostUsd`.
- These figures are estimates. For a hard stop, also set a spend cap in AI Studio or a budget in Cloud Billing.

## HTTP mode

```bash
GEMINI_API_KEY=... gemini-media-mcp serve --transport http                 # http://127.0.0.1:8765/mcp
GEMINI_MEDIA_HTTP_TOKEN=secret gemini-media-mcp serve --transport http --http-addr 0.0.0.0:8765
claude mcp add --transport http gemini-media http://127.0.0.1:8765/mcp --header "Authorization: Bearer secret"
```

- HTTP mode is stateless Streamable HTTP, which MCP 2026-07-28 requires; older clients still work.
- The server protects against DNS rebinding and cross-origin requests.
- It refuses to listen on a non-loopback address without a token.
- It only reads input files from the output directory or `GEMINI_MEDIA_INPUT_DIRS` (unless `GEMINI_MEDIA_ALLOW_ANY_INPUT_PATH=true`).
- `GET /healthz` reports status.

## Skills

The [`skills/`](skills) directory contains [Agent Skills](https://agentskills.io) that teach agents the full workflow for each media type: intent, prompt craft, model choice, cost checks, review and iteration. They cover interactive use as well as unattended runs.

| Skill | For |
|---|---|
| `gemini-image` | Images: generation, editing, multi-reference composition, text rendering |
| `gemini-video` | Video: text/image-to-video, frame interpolation, reference ingredients, Omni editing, extension, async jobs |
| `gemini-speech` | Voiceovers, narration, two-speaker dialogue, voice and style selection |
| `gemini-music` | Clips and full songs with structure, lyrics and tempo |
| `gemini-media-production` | Multi-asset projects (storyboard → keyframes → video → voiceover → music → ffmpeg assembly) with a budget plan |

Plugin installs (Claude Code, Codex, Gemini CLI, VS Code) include the skills. To install them in any Agent Skills–compatible agent, run `npx skills add mordor-forge/gemini-media-mcp`, or copy the folders into `.agents/skills/` or `~/.claude/skills/`.

## Development

Paid live E2E tests are run locally with your own API key. CI runs the free
checks and compiles/vets the E2E tests, but never executes live generation,
including after merges to `main` or on manual workflow runs.

```bash
go build ./...
go test -race ./...
golangci-lint run
python3 scripts/validate-skills.py skills
GEMINI_MEDIA_E2E=1 GEMINI_API_KEY=... go test -tags=e2e ./internal/media/ -run E2E -v -timeout 30m   # live, costs cents
# add GEMINI_MEDIA_E2E_VIDEO=1 for the video test (~$0.20), GEMINI_MEDIA_E2E_OMNI=1 for the Omni clip + edit (~$0.30),
# GEMINI_MEDIA_E2E_OUTPUT_DIR=./e2e-out to keep the files
```

- [AGENTS.md](AGENTS.md): guide for coding agents and contributors.
- [docs/architecture-review.md](docs/architecture-review.md): architecture and design decisions.
- [internal/catalog/models.yaml](internal/catalog/models.yaml): model catalog. When Google changes models, edit this file, not the code.

## License

[Apache-2.0](LICENSE)
