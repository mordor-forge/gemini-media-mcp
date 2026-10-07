---
name: gemini-video
description: Generates and edits short video clips with native audio using Google Veo and Gemini Omni Flash through the gemini-media MCP server - text-to-video, animating a still image, first-to-last-frame transitions, keeping a character or product consistent from reference images, changing an existing clip with an instruction, and extending clips into longer shots. Handles the asynchronous job flow, resolution and duration rules, and cost approval for this expensive medium. Use when the user wants a video, clip, animation, b-roll, cinematic shot or product spin, wants to bring a picture to life, or wants to add, remove or restyle something in a short clip. Not for trimming or cutting footage, screen recordings, slideshows, or audio-only work.
license: Apache-2.0
compatibility: Requires the gemini-media MCP server (https://github.com/mordor-forge/gemini-media-mcp) with a Gemini API key or a Vertex AI project (Vertex AI express mode cannot run Veo).
metadata:
  author: mordor-forge
  version: "1.0.0"
  mcp-server: gemini-media
---

# Gemini Video (Veo and Omni)

## Quick start

1. Write one prompt covering subject and action, camera, setting and lighting, style, and sound.
2. Call `gemini-media:generate_video` with `prompt` (defaults: model `lite`, 720p, 8 s, 16:9). It returns a `jobId` with `state: "working"`.
3. Call `gemini-media:get_video` with the `jobId` and `waitSeconds: 45`. Each call blocks server-side; repeat while `state` is `working`. Veo usually takes 1-3 minutes, Omni 1-5 (4K and extensions longer). You cannot sleep between calls, so just call again.
4. When `state` is `completed`, the MP4 is already downloaded: use `files[0].path` / `files[0].uri`.
5. Review (see Review), then iterate or extend.

## Tools

The tools live on the `gemini-media` MCP server. Harnesses name MCP tools differently (`mcp__gemini-media__generate_video`, `gemini-media.generate_video`, or plain `generate_video`); match on the part after the server name. This skill writes them as `gemini-media:<tool>`.

| Tool | Use it to |
|------|-----------|
| `gemini-media:generate_video` | Start a clip from text, a first frame, first+last frames, or reference images (Veo 4-8 s, Omni 3-10 s) |
| `gemini-media:get_video` | Wait for a job (default 45 s per call) and download the result; safe to repeat |
| `gemini-media:edit_video` | Change a finished clip (`jobId`) or a video file of up to 10 s with an instruction (Omni) |
| `gemini-media:extend_video` | Continue a completed clip: `omni` up to 10 s per call (40 s total), `fast`/`standard` about 7 s |
| `gemini-media:estimate_cost` | Price a clip before starting it (`compare: true` prices every video model) |
| `gemini-media:list_models` | Current video models, prices and rules (`detail: true`) |
| `gemini-media:get_usage` | Spend, budget, and still-running video jobs (recover a lost `jobId`) |

Image inputs (`image`, `lastFrame`, `referenceImages`) accept a file path, a `gemini-media://` URI from an earlier result, or a `data:` URI. Web URLs are not fetched.

## Models at a glance

| Alias | Choose it for | Limits |
|-------|---------------|--------|
| `lite` (default) | Drafts, previews, social clips | 720p/1080p; first frame and first+last frame; no 4k, no reference images, no extension |
| `omni` | Strongest prompt adherence, readable text, multi-shot scenes, editing and long extensions | Gemini API only; 3-10 s; 360p drafts, 720p, 1080p/4k (upscaled); up to 10 reference images; about `fast`'s price at 720p |
| `fast` | Best value for production clips | 720p/1080p/4k; up to 3 reference images; extension |
| `standard` | Hero shots and final renders | Same features as `fast`, several times the price |

Prices change: call `gemini-media:list_models` (`mediaType: "video"`) and `gemini-media:estimate_cost`; never quote prices from memory.

## Parameters and rules

| Parameter | Rule |
|-----------|------|
| `durationSeconds` | Veo: 4, 6 or 8 (default 8); must be 8 for 1080p, 4k and reference images. Omni: 3-10 (default 8). |
| `resolution` | `720p` default; `1080p`; `4k` on `fast`/`standard`/`omni`. Omni also has `360p` for fast, cheap drafts. Veo extensions are always 720p. |
| `aspectRatio` | `16:9` (default) or `9:16` only. Make input images the same ratio. |
| `image` | First frame. The clip starts exactly on it. |
| `lastFrame` | Final frame; requires `image`. Veo interpolates between them. |
| `referenceImages` | Veo: up to 3 "ingredients" (a person, character, product) to keep consistent; `fast`/`standard` only, 8 s only, not combinable with `image`/`lastFrame`. Omni: up to 10 images including frames; name them `<IMAGE_REF_0>`, `<IMAGE_REF_1>`... in the prompt. |
| `negativePrompt` | Things to avoid as plain nouns: `"text overlays, watermark, blur"`. Omni has no field for it, so the server appends "Avoid: ..." to the prompt. |
| `personGeneration` | Leave unset. On the Gemini API only `allow_all` (text-to-video) and `allow_adult` (image-based modes) are accepted. |
| `seed`, `generateAudio` | Vertex AI Veo only (dropped with a warning elsewhere; the Gemini API and Omni always generate audio). |
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
5. **Extension:** `gemini-media:extend_video` with the completed `jobId` and a prompt for what happens next. It returns a new `jobId`: poll it with `gemini-media:get_video`.
   - `omni` clips: up to 10 s per call, 40 s total. Omni reads the last 10 s for context, so characters, motion and audio stay coherent; prompts like "The scene continues: ..." work best.
   - `fast`/`standard` clips: within 2 days of generation; about 7 s at 720p per call, up to 148 s total. The result is the whole video so far, so keep only the latest file; do not concatenate.
   - To continue a `lite` clip or an expired job, extract its last frame (see **gemini-media-production**) and use it as `image` for a new clip.
6. **Editing (Omni):** `gemini-media:edit_video` with the `jobId` of a finished clip, or `video` (a file of at most 10 s and 20 MB). Say the change simply and end with "Keep everything else the same." Omni clips are edited in conversation (no re-upload), so several small edits in a row work; other clips are sent as their saved file. Each edit is a new billed clip. Editing uploaded videos is not available in the EEA, Switzerland, the UK and some US states.

Recipes for each mode: [references/prompt-guide.md](references/prompt-guide.md).

## Prompt craft

- Cover the elements Veo responds to: **subject, context, action, style, camera motion, composition, ambiance**.
- Describe motion explicitly and in time order: "the camera slowly dollies in as she turns toward the window".
- One shot, one main action. A clip is at most 8 s; split sequences into separate shots.
- **Audio is generated from the prompt.** Put dialogue in quotes after the speaker, with delivery in parentheses: `Woman: (voice tight with fear) "Then what is it?"`. Describe sound effects ("snapping twigs, footsteps on damp earth"), ambience ("a lone bird chirps") and music ("soft solo piano"). About 8 s fits one or two short lines.
- If you will add your own voiceover or music later, ask for "natural ambient sound only" and consider `negativePrompt: "music, speech"`.
- Rendered text, logos and UI are unreliable in Veo: add them in post, or put them in the first frame image. Omni renders short text legibly; quote it exactly.
- Omni tends to cut between several shots. Ask for "a continuous, unbroken shot" when you want one, or time the beats yourself: `[0-3s] ... [3-6s] ... [6-10s] ...`.

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

- Video is the most expensive medium here. Draft with `lite` at 720p, or `omni` at `360p` (about a third of its 720p price), 4-6 s while the idea is still loose; move to `fast`, `standard`, `omni` 720p, 1080p or 4k only for finals.
- Before any `fast`/`standard`/`omni` clip, any 1080p/4k clip, reference-image clips, extensions, edits, or more than one clip, call `gemini-media:estimate_cost` with `mediaType: "video"`, `model`, `resolution`, `durationSeconds`, `count` and `compare: true`. Tell the user the per-clip and total figure, and for multi-clip plans get agreement before starting.
- `[confirmation]` error: the call exceeds the approval threshold. Ask the user with the quoted amount; only after they agree, retry the same call with `approvedCostUsd`. Never invent approval.
- `[budget]` error: a spending cap was hit. Stop and show `gemini-media:get_usage`.
- Cost is reserved while a job runs and settled when it finishes; `get_usage` shows it.

## Errors

Tool errors read `[kind] message` then `Hint: ...`. Follow the hint; in short:

| Kind | Do |
|------|----|
| `invalid` | Fix the rule named in the message (duration vs resolution, 4k on `lite`, references with a first frame, extending a running or `lite` job, a video too long to edit). `gemini-media:list_models` with `detail: true` shows the rules. |
| `safety` or state `filtered` | Rephrase: remove real people, brands, copyrighted characters, violence; for image-to-video, check the input image. Do not resend unchanged. |
| `quota`, `unavailable` | The server already retried with backoff. Wait briefly, retry once, then report. |
| `timeout` | While waiting, the job keeps running server-side: call `gemini-media:get_video` again rather than starting a new job. If starting a job timed out, Google may still have accepted it, so a retry can double-charge: retry once at most. |
| `not_found` | Unknown or expired `jobId`, retired model, missing input file, or `omni` on Vertex AI (Gemini API only). Regenerate, check the path, or use a Veo model. |
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
