---
name: gemini-video
description: Generates short video clips with native audio using Google Veo through the gemini-media MCP server - text-to-video, animating a still image, first-to-last-frame transitions, keeping a character or product consistent from reference images, and extending clips into longer shots. Handles the asynchronous job flow, resolution and duration rules, and cost approval for this expensive medium. Use when the user wants a video, clip, animation, b-roll, cinematic shot or product spin, or wants to bring a picture to life. Not for editing or trimming existing footage, screen recordings, slideshows, or audio-only work.
license: Apache-2.0
compatibility: Requires the gemini-media MCP server (https://github.com/mordor-forge/gemini-media-mcp) with a Gemini API key or a Vertex AI project (Vertex AI express mode cannot run Veo).
metadata:
  author: mordor-forge
  version: "1.0.0"
  mcp-server: gemini-media
---

# Gemini Video (Veo)

## Quick start

1. Write one prompt covering subject and action, camera, setting and lighting, style, and sound.
2. Call `gemini-media:generate_video` with `prompt` (defaults: model `lite`, 720p, 8 s, 16:9). It returns a `jobId` with `state: "working"`.
3. Call `gemini-media:get_video` with the `jobId` and `waitSeconds: 45`. Each call blocks server-side; repeat while `state` is `working`. Veo usually takes 1-3 minutes. You cannot sleep between calls, so just call again.
4. When `state` is `completed`, the MP4 is already downloaded: use `files[0].path` / `files[0].uri`.
5. Review (see Review), then iterate or extend.

## Tools

The tools live on the `gemini-media` MCP server. Harnesses name MCP tools differently (`mcp__gemini-media__generate_video`, `gemini-media.generate_video`, or plain `generate_video`); match on the part after the server name. This skill writes them as `gemini-media:<tool>`.

| Tool | Use it to |
|------|-----------|
| `gemini-media:generate_video` | Start a 4-8 s clip from text, a first frame, first+last frames, or up to 3 reference images |
| `gemini-media:get_video` | Wait for a job (default 45 s per call) and download the result; safe to repeat |
| `gemini-media:extend_video` | Continue a completed `fast`/`standard` clip by about 7 s |
| `gemini-media:estimate_cost` | Price a clip before starting it (`compare: true` prices every Veo model) |
| `gemini-media:list_models` | Current Veo models, prices and rules (`detail: true`) |
| `gemini-media:get_usage` | Spend, budget, and still-running video jobs (recover a lost `jobId`) |

Image inputs (`image`, `lastFrame`, `referenceImages`) accept a file path, a `gemini-media://` URI from an earlier result, or a `data:` URI. Web URLs are not fetched.

## Models at a glance

| Alias | Choose it for | Limits |
|-------|---------------|--------|
| `lite` (default) | Drafts, previews, social clips | 720p/1080p; first frame and first+last frame; no 4k, no reference images, no extension |
| `fast` | Best value for production clips | 720p/1080p/4k; reference images; extension |
| `standard` | Hero shots and final renders | Same features as `fast`, several times the price |

Prices change: call `gemini-media:list_models` (`mediaType: "video"`) and `gemini-media:estimate_cost`; never quote prices from memory.

## Parameters and rules

| Parameter | Rule |
|-----------|------|
| `durationSeconds` | 4, 6 or 8 (default 8). Must be 8 for 1080p, 4k and reference images. |
| `resolution` | `720p` default; `1080p`; `4k` on `fast`/`standard` only. Extensions are always 720p. |
| `aspectRatio` | `16:9` (default) or `9:16` only. Make input images the same ratio. |
| `image` | First frame. The clip starts exactly on it. |
| `lastFrame` | Final frame; requires `image`. Veo interpolates between them. |
| `referenceImages` | Up to 3 "ingredients" (a person, character, product) to keep consistent. `fast`/`standard` only, 8 s only, not combinable with `image`/`lastFrame`. |
| `negativePrompt` | Things to avoid as plain nouns: `"text overlays, watermark, blur"`. |
| `personGeneration` | Leave unset. On the Gemini API only `allow_all` (text-to-video) and `allow_adult` (image-based modes) are accepted. |
| `seed`, `generateAudio` | Vertex AI only (dropped with a warning on the Gemini API, which always generates audio). |
| `waitSeconds` | On `gemini-media:generate_video` defaults to 0 (return the job at once). Keep any wait below your client's tool timeout. |

The prompt is limited to about 1,024 tokens; a tight paragraph works better anyway.

## Job handling

- `state` is `working`, `completed`, `failed` or `filtered`. Follow the `next` field in each result.
- `working`: call `gemini-media:get_video` again with `waitSeconds: 45` (use 25 if your client times out sooner). Never resubmit `gemini-media:generate_video` for a job that is still running: every submission is billed.
- `completed`: `files` holds the MP4 (with `durationSeconds`); `remoteExpiresAt` says when Google deletes its copy, after which the clip can no longer be extended.
- `failed`: read `error`, change the prompt or inputs, then generate again.
- `filtered`: blocked by safety filters. Rephrase (see Errors) rather than retrying unchanged.
- Several shots: start each job, then poll each `jobId` in turn. If you lose a `jobId`, `gemini-media:get_usage` lists running video jobs.

## Modes

1. **Text-to-video:** prompt only.
2. **Image-to-video:** `image` = first frame (often from **gemini-image**). Describe the motion and what happens next; do not re-describe the whole picture.
3. **First and last frame:** `image` + `lastFrame`. Describe the transition or action that connects them. Both frames should share style, lighting and aspect ratio.
4. **Ingredients:** `referenceImages` (up to 3, `fast`/`standard`, 8 s). Name each ingredient in the prompt ("the woman, the flamingo dress and the heart-shaped sunglasses from the references").
5. **Extension:** `gemini-media:extend_video` with the completed `jobId` and a prompt for what happens next. Works on `fast`/`standard` clips generated within the last 2 days; adds about 7 s at 720p per call, up to 148 s total. It returns a new `jobId`: poll it with `gemini-media:get_video`. The result is the whole video so far, so keep only the latest file; do not concatenate.
   - To continue a `lite` clip or an expired job, extract its last frame (see **gemini-media-production**) and use it as `image` for a new clip.

Recipes for each mode: [references/prompt-guide.md](references/prompt-guide.md).

## Prompt craft

- Cover the elements Veo responds to: **subject, context, action, style, camera motion, composition, ambiance**.
- Describe motion explicitly and in time order: "the camera slowly dollies in as she turns toward the window".
- One shot, one main action. A clip is at most 8 s; split sequences into separate shots.
- **Audio is generated from the prompt.** Put dialogue in quotes after the speaker, with delivery in parentheses: `Woman: (voice tight with fear) "Then what is it?"`. Describe sound effects ("snapping twigs, footsteps on damp earth"), ambience ("a lone bird chirps") and music ("soft solo piano"). About 8 s fits one or two short lines.
- If you will add your own voiceover or music later, ask for "natural ambient sound only" and consider `negativePrompt: "music, speech"`.
- Rendered text, logos and UI are unreliable in video: add them in post, or put them in the first frame image.

Examples:

```text
generate_video(prompt:
"Close-up of melting icicles on a frozen rock wall, cool blue tones. The camera slowly
zooms in on water drips falling in slow motion. Sound: steady dripping and a faint wind.")

generate_video(model: "fast", image: "gemini-media://files/sink-surfers.png", prompt:
"A surreal cinematic macro shot. Tiny surfers ride rolling waves inside the stone sink as the
brass faucet keeps the surf going. The camera slowly pans across the sunlit scene.
Sound: splashing water and faint cheering.")

generate_video(prompt:
"Paper cut-out animation. In a candlelit library, a nervous new librarian whispers to an old
curator. New Librarian: \"Where do you keep the forbidden books?\" Old Curator: (smiling) \"We don't. They keep us.\"
Creaking shelves and a ticking clock.")
```

## Review

Most agents cannot watch video. Check what you can: `state`, `durationSeconds`, `warnings`, and the prompt you sent. If your environment has a shell with ffmpeg, extract a few frames and inspect them as images (recipes in **gemini-media-production**). Tell the user honestly what you verified and ask them to watch it when they are present.

## Cost and approvals

- Video is the most expensive medium here. Draft with `lite` at 720p (4-6 s when the idea is still loose); move to `fast`, `standard`, 1080p or 4k only for finals.
- Before any `fast`/`standard` clip, any 1080p/4k clip, reference-image clips, extensions, or more than one clip, call `gemini-media:estimate_cost` with `mediaType: "video"`, `model`, `resolution`, `durationSeconds`, `count` and `compare: true`. Tell the user the per-clip and total figure, and for multi-clip plans get agreement before starting.
- `[confirmation]` error: the call exceeds the approval threshold. Ask the user with the quoted amount; only after they agree, retry the same call with `approvedCostUsd`. Never invent approval.
- `[budget]` error: a spending cap was hit. Stop and show `gemini-media:get_usage`.
- Cost is reserved while a job runs and settled when it finishes; `get_usage` shows it.

## Errors

Tool errors read `[kind] message` then `Hint: ...`. Follow the hint; in short:

| Kind | Do |
|------|----|
| `invalid` | Fix the rule named in the message (duration vs resolution, 4k on `lite`, references with a first frame, extending a running or `lite` job). `gemini-media:list_models` with `detail: true` shows the rules. |
| `safety` or state `filtered` | Rephrase: remove real people, brands, copyrighted characters, violence; for image-to-video, check the input image. Do not resend unchanged. |
| `quota`, `unavailable` | The server already retried with backoff. Wait briefly, retry once, then report. |
| `timeout` | While waiting, the job keeps running server-side: call `gemini-media:get_video` again rather than starting a new job. If starting a job timed out, Google may still have accepted it, so a retry can double-charge: retry once at most. |
| `not_found` | Unknown or expired `jobId`, retired model, or missing input file. Regenerate or check the path. |
| `auth`, `permission` | Stop. Ask the user to run `gemini-media-mcp doctor` (Vertex AI express mode cannot generate video). |
| `budget`, `confirmation` | See Cost and approvals. |

Never loop on errors that cost money; one retry at most.

## Interaction mode

- **With a user present:** confirm the essentials in one line when they are unclear (orientation, length, draft or final quality) and the estimated cost for anything beyond a `lite` draft. After delivery, offer two or three next steps (extend, upgrade tier, new take with a changed camera move).
- **Autonomous (nobody to ask):** do not block on questions. Use `lite`, 720p, a sensible duration and ratio; stay below the confirmation threshold and never set `approvedCostUsd` yourself; poll with `gemini-media:get_video` until the job finishes. Report the prompt, model, parameters, file path, cost, and what a higher-quality version would cost.

## Chaining

- First frames, last frames and ingredient images: **gemini-image** (generate at 16:9 or 9:16).
- Voiceover beyond in-clip dialogue: **gemini-speech**. Score or music bed: **gemini-music**.
- Multi-shot projects, consistent characters across shots, and ffmpeg assembly: **gemini-media-production**.

## References

- [references/prompt-guide.md](references/prompt-guide.md): camera moves, shot types, lighting, audio cue syntax, and recipes for frame interpolation, ingredients and extension continuity.
