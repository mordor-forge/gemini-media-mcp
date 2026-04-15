# gemini-media-mcp

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![gemini-media-mcp MCP server](https://glama.ai/mcp/servers/mordor-forge/gemini-media-mcp/badges/score.svg)](https://glama.ai/mcp/servers/mordor-forge/gemini-media-mcp)

Unified Go MCP server for AI media generation via Google Gemini API and Vertex AI.

[![gemini-media-mcp MCP server](https://glama.ai/mcp/servers/mordor-forge/gemini-media-mcp/badges/card.svg)](https://glama.ai/mcp/servers/mordor-forge/gemini-media-mcp)

## Features

- **Image generation** -- text-to-image with configurable aspect ratios and resolutions (1K/2K/4K)
- **Image editing** -- modify existing images with natural language prompts
- **Multi-reference composition** -- combine up to 3 reference images with style/content guidance
- **Video generation** -- text-to-video via Veo 3.1 Lite, Fast, and Standard tiers
- **Image-to-video** -- animate still images into video clips
- **Video extension** -- chain clips for longer content (Fast and Standard tiers)
- **Text-to-speech** -- generate spoken audio with configurable voices and languages
- **Music generation** -- AI music via Lyria 3 (30s clips or full songs with vocals, structure control)
- **Single binary** -- no runtime dependencies, runs over stdio transport
- **Provider abstraction** -- backend-agnostic interfaces for image, video, audio, and model operations
- **Dual backend** -- supports both Gemini API (API key) and Vertex AI (project credentials)

## Quick Start

```bash
# Install
go install github.com/mordor-forge/gemini-media-mcp/cmd/gemini-media-mcp@latest

# Configure (Gemini API; either variable name works)
export GEMINI_API_KEY="your-api-key"
# export GOOGLE_API_KEY="your-api-key"

# Or configure (Vertex AI)
export GOOGLE_CLOUD_PROJECT="your-project-id"
export GOOGLE_CLOUD_LOCATION="us-central1"

# Run directly (stdio transport)
gemini-media-mcp
```

Then add it to your MCP client -- see [MCP Client Configuration](#mcp-client-configuration) below.

## Configuration

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `GOOGLE_API_KEY` | Yes* | -- | Gemini API key. `GEMINI_API_KEY` is also accepted |
| `GOOGLE_CLOUD_PROJECT` | Yes* | -- | GCP project ID for Vertex AI backend |
| `GOOGLE_CLOUD_LOCATION` | No | `us-central1` | GCP region for Vertex AI |
| `MEDIA_OUTPUT_DIR` | No | `~/generated_media` | Directory for saved media files |

*One of `GOOGLE_API_KEY` or `GOOGLE_CLOUD_PROJECT` must be set. If both are set, API key takes precedence (avoids conflicts when `GOOGLE_CLOUD_PROJECT` is set in the shell for other tools).

If you're unsure which backend is active, call `get_config` from your MCP client to confirm the selected backend and output directory.

## Available Tools

| Tool | Description | Type |
|------|-------------|------|
| `generate_image` | Generate image from text prompt | Sync |
| `edit_image` | Edit existing image with text prompt | Sync |
| `compose_images` | Multi-reference image composition (up to 3) | Sync |
| `generate_video` | Generate video from text prompt (returns operation ID) | Async |
| `animate_image` | Animate image into video (first frame) | Async |
| `extend_video` | Chain video clips for longer content | Async |
| `video_status` | Check video generation progress | Sync |
| `download_video` | Download completed video | Sync |
| `generate_audio` | Generate spoken audio from text (TTS) | Sync |
| `generate_music` | Generate AI music from text description (Lyria) | Sync |
| `list_models` | Show available models with capabilities and pricing | Sync |
| `get_config` | Show current backend and configuration | Sync |

Async tools return an operation ID immediately. Use `video_status` to poll for completion, then `download_video` to retrieve the file.

## Model Tiers

### Image

| Tier | Model | Best For | Cost |
|------|-------|----------|------|
| nb2 (default) | `gemini-3.1-flash-image-preview` | Quick iterations, most tasks | ~$0.067/img |
| pro | `gemini-3-pro-image-preview` | Final renders, complex scenes | ~$0.134/img |

Both tiers support resolutions 1K, 2K, 4K and aspect ratios 1:1, 2:3, 3:2, 3:4, 4:3, 4:5, 5:4, 9:16, 16:9, 21:9.

### Video

| Tier | Model | Best For | Cost |
|------|-------|----------|------|
| lite (default) | `veo-3.1-lite-generate-preview` | High-volume, drafts | $0.05/sec (720p), $0.08/sec (1080p) |
| fast | `veo-3.1-fast-generate-preview` | Good quality iterations | $0.15/sec (720p/1080p), $0.35/sec (4k) |
| standard | `veo-3.1-generate-preview` | Final renders, 4K | $0.40/sec (720p/1080p), $0.60/sec (4k) |

Supported aspect ratios are `16:9` and `9:16`. Supported durations are `4`, `6`, and `8` seconds. Lite supports `720p` and `1080p`. Fast and Standard support `720p`, `1080p`, and `4K`. Video extension (`extend_video`) is only available on Fast and Standard tiers, and the extension tier must match the original generation.

### Audio (TTS)

| Tier | Model | Best For | Cost |
|------|-------|----------|------|
| tts | `gemini-2.5-flash-preview-tts` | Text-to-speech with natural voices | Standard Gemini token pricing |

The `generate_audio` tool converts text to spoken audio. It supports:

- **Voice selection** -- Choose from prebuilt voices like `Aoede`, `Kore`, `Puck`, and more. Default: `Aoede`
- **Language** -- Set the language code (e.g., `en-US`, `it-IT`, `cs-CZ`, `de-DE`). Default: `en-US`
- **Natural speech** -- Generates expressive, natural-sounding speech with appropriate pacing and intonation

Output is saved as raw PCM audio (`audio/L16`, 24kHz sample rate). The file can be played with tools like `ffplay` or converted to other formats:

```bash
# Play directly
ffplay -f s16le -ar 24000 -ac 1 ~/generated_media/audio-2026-04-02T12-20-12-0603.pcm

# Convert to WAV
ffmpeg -f s16le -ar 24000 -ac 1 -i audio.pcm audio.wav

# Convert to MP3
ffmpeg -f s16le -ar 24000 -ac 1 -i audio.pcm audio.mp3
```

### Music (Lyria)

| Tier | Model | Output | Best For | Cost |
|------|-------|--------|----------|------|
| clip (default) | `lyria-3-clip-preview` | 30-second clips | Quick iterations, sound design | ~$0.08/song |
| full | `lyria-3-pro-preview` | Up to ~3 minutes | Full songs with vocals, verses, choruses | Token-based |

The `generate_music` tool creates AI-generated music from text descriptions. Capabilities include:

- **Genre and style** -- specify any genre, instruments, BPM, key/scale, mood
- **Structure control** -- use tags like `[Verse]`, `[Chorus]`, `[Bridge]`, `[Intro]`, `[Outro]`
- **Custom lyrics** -- include lyrics with section markers for vocal tracks
- **Timestamp control** -- `[0:00 - 0:10] Intro: gentle piano...` for precise section timing
- **Multi-language** -- prompt language determines output language
- **High fidelity** -- 48kHz stereo MP3 output

All generated music is watermarked with SynthID.

**Example prompts:**

```
# Instrumental
"A gentle acoustic guitar melody in C major, 90 BPM, calm and peaceful indie folk"

# With structure
"[Intro] Ambient synth pad, ethereal
[Verse] Lo-fi hip-hop beat, mellow piano chords, vinyl crackle
[Chorus] Uplifting, add strings and gentle drums
[Outro] Fade out with reverb"

# With lyrics
"Upbeat pop song, 120 BPM, major key
[Chorus] We're dancing in the light / Everything feels right / Under stars so bright tonight"
```

You can pass the tier name (`lite`, `fast`, `standard`, `nb2`, `pro`, `tts`, `clip`, `full`) or a raw model ID directly.

## MCP Client Configuration

### Claude Code

Add to your Claude Code MCP settings (`~/.claude/settings.json` or project `.mcp.json`):

```json
{
  "mcpServers": {
    "gemini-media": {
      "command": "gemini-media-mcp",
      "env": {
        "GOOGLE_API_KEY": "your-api-key",
        "MEDIA_OUTPUT_DIR": "/path/to/output"
      }
    }
  }
}
```

Use either `GOOGLE_API_KEY` or `GEMINI_API_KEY` in the `env` block above; both are accepted.

Or if building from source:

```json
{
  "mcpServers": {
    "gemini-media": {
      "command": "/path/to/gemini-media-mcp",
      "env": {
        "GOOGLE_API_KEY": "your-api-key"
      }
    }
  }
}
```

## Companion Skills for Claude Code

The `skills/` directory contains Claude Code skills that provide interactive workflows on top of the MCP tools. Each skill guides Claude through prompt engineering, model selection, and iterative refinement for a specific media type.

| Skill | Directory | Description |
|-------|-----------|-------------|
| **gemini-image-gen** | `skills/gemini-image-gen/` | Image generation, editing, and multi-reference composition |
| **video-gen** | `skills/video-gen/` | Video generation with async polling, image-to-video, extension |
| **music-gen** | `skills/music-gen/` | Music generation with structure tags, lyrics, genre control |
| **tts-gen** | `skills/tts-gen/` | Text-to-speech with voice and language selection |

To install a skill, copy its directory to `~/.claude/skills/`:

```bash
cp -r skills/video-gen ~/.claude/skills/
cp -r skills/music-gen ~/.claude/skills/
cp -r skills/tts-gen ~/.claude/skills/
cp -r skills/gemini-image-gen ~/.claude/skills/
```

Skills are optional — the MCP tools work without them. But the skills add prompt engineering guidance, model tier recommendations, and interactive review workflows that significantly improve output quality.

## Building from Source

```bash
git clone https://github.com/mordor-forge/gemini-media-mcp.git
cd gemini-media-mcp
go build ./cmd/gemini-media-mcp/
```

The binary will be created at `./gemini-media-mcp`.

To run tests:

```bash
go test ./...
```

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/your-feature`)
3. Make your changes and add tests
4. Run `go test ./...` and `go vet ./...`
5. Commit your changes
6. Open a pull request against `main`

## License

[Apache-2.0](LICENSE)
