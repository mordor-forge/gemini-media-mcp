---
name: gemini-image
description: Generates and edits images with Google's Nano Banana models through the gemini-media MCP server - text-to-image, photo edits and retouching, multi-reference composition, product shots, posters, logos and infographics with legible text, stickers and icons, variations, and search-grounded visuals of current events or real places. Use when the user wants to create, draw, design, mock up, restyle, relight, extend or combine pictures, or needs keyframes for a video, even if they never mention Gemini. Not for charts plotted from data, SVG or code-drawn graphics, video clips (gemini-video) or audio.
license: Apache-2.0
compatibility: Requires the gemini-media MCP server (https://github.com/mordor-forge/gemini-media-mcp) with a Gemini API key or Vertex AI project.
metadata:
  author: mordor-forge
  version: "1.0.0"
  mcp-server: gemini-media
---

# Gemini Image (Nano Banana)

## Quick start

1. Write the prompt as full sentences: subject, setting, composition, lighting, style and purpose. Put exact text to render in quotes.
2. Call `gemini-media:generate_image` with `prompt` and, if the destination implies one, `aspectRatio`. Keep the default model (`nb2`) and size (`1K`) for a first pass.
3. Review the result: inspect the preview returned with it (if your client renders images), or open the saved file if your environment can view images. If you cannot see images, say so instead of guessing what it looks like.
4. Refine with `gemini-media:edit_image`, passing the result's `uri` as `image` and one specific change.
5. Report the file path(s), model and cost from the result.

## Tools

The tools live on the `gemini-media` MCP server. Harnesses name MCP tools differently (`mcp__gemini-media__generate_image`, `gemini-media.generate_image`, or plain `generate_image`); match on the part after the server name. This skill writes them as `gemini-media:<tool>`.

| Tool | Use it to |
|------|-----------|
| `gemini-media:generate_image` | Create 1-4 images from text, optionally guided by up to 14 reference images |
| `gemini-media:edit_image` | Change an existing image (add/remove/replace, restyle, relight, fix text, change aspect ratio); saves a new file |
| `gemini-media:estimate_cost` | Price a request before running it (`compare: true` prices every image model) |
| `gemini-media:list_models` | Current models, aliases, prices; `detail: true` adds sizes and aspect ratios |
| `gemini-media:get_usage` / `gemini-media:get_config` | Spend so far and budget; backend, output directory and input-file policy |

Results carry `files[]` (each with `path` on the server's disk, a `gemini-media://` `uri`, width and height), `model`, `cost`, optional `text` (the model's own commentary) and `warnings` (read them: they report dropped parameters and retired-model redirects). Inputs accept a file path, a `gemini-media://` URI or bare file name of an earlier output, or a `data:` URI. Web URLs are not fetched: download the file first.

## Models at a glance

| Alias | Choose it for | Limits |
|-------|---------------|--------|
| `nb2` (default, Nano Banana 2.1) | Almost everything, drafts through finals, including most text in images | 1K to 4K; extra ratios 1:4, 4:1, 1:8, 8:1; `googleSearch` on the Gemini API only |
| `pro` | Dense infographics, complex multi-subject scenes, final renders when nb2 falls short | 1K to 4K; about three to four times the price of nb2 at 1K; slower |
| `nb2-lite` | Bulk drafts and thumbnails at scale | 1K only; same per-image price as nb2 but cheaper input; no `googleSearch`; weaker with multiple references |

Prices and capabilities change: call `gemini-media:list_models` (`mediaType: "image"`) for the current list and `gemini-media:estimate_cost` for a specific request. Never quote prices from memory.

## Parameters that matter

| Parameter | Guidance |
|-----------|----------|
| `aspectRatio` | Match the destination (table below). Defaults to 1:1 without reference images; with them the model usually follows the first image's ratio. |
| `imageSize` | `1K` default (also the smallest size), `2K` for most finals, `4K` only when print or large display needs it. |
| `referenceImages` | Subjects, products, characters or style to carry over. Refer to them by order in the prompt ("the mug in image 1"). |
| `count` | 1-4 parallel variations, each billed. Use it instead of asking for "four versions" in the prompt; the model does not reliably honor counts written in text. |
| `googleSearch` | Ground in live information (scores, weather, recent events, real landmarks, data for an infographic). Pair with `pro` for text-heavy results. |
| `outputName` | Readable base file name, e.g. `hero-mug-v2`. Edits default to the source name plus `-edit`. |
| `edit_image.aspectRatio` | Set a new ratio to outpaint (extend the canvas); omit to keep the source ratio. |
| `edit_image.model` | Omit: the server keeps editing with the model that made the source. |

| Destination | aspectRatio |
|-------------|-------------|
| Square post, avatar, icon, sticker | 1:1 |
| Instagram portrait, feed ad | 4:5 |
| Story, Reel, TikTok, phone wallpaper, 9:16 video keyframe | 9:16 |
| YouTube thumbnail, slide, desktop, 16:9 video keyframe | 16:9 |
| Poster, book cover | 2:3 or 3:4 |
| Cinematic banner | 21:9 (web banners and skyscrapers: 4:1, 8:1, 1:4, 1:8 on nb2) |

## Workflow

1. **Intent.** Pin down subject, purpose and destination (drives ratio and size), style, any exact text, and what must be preserved (which becomes reference images).
2. **Prompt.** Apply the prompt craft below. Pick a template from [references/prompt-patterns.md](references/prompt-patterns.md) for common jobs.
3. **Model and parameters.** Default `nb2` at 1K. Switch to `pro` when text accuracy or scene complexity matters. Explore with `count: 2-4` or `nb2-lite`.
4. **Generate** with `gemini-media:generate_image`.
5. **Review** against the intent: requested elements present, text spelled exactly, hands and faces, composition, nothing extra. Read the returned `text` and `warnings`.
6. **Iterate** with `gemini-media:edit_image`: one specific change per call, and say what must stay unchanged. If quality drifts after three or four edits, regenerate from a refined prompt that folds in what you learned, passing the best result as a reference.
7. **Finalize.** For a high-resolution master, re-run the winning prompt at `2K`/`4K`, or edit the chosen image with `imageSize` set and "keep everything unchanged" (expect small differences). Deliver path, `uri`, model and cost.

## Prompt craft

- **Describe the scene; do not list keywords.** A narrative paragraph beats comma-separated tags. Skip filler like "4k, masterpiece, trending".
- **Be specific.** "Ornate elven plate armor etched with silver leaf, high collar, falcon-wing pauldrons" rather than "fantasy armor".
- **State the purpose.** "A logo for a high-end minimalist skincare brand" steers better than "a logo".
- **Phrase exclusions positively.** "An empty, deserted street" instead of "no cars".
- **Direct the camera** for photoreal work: lens, angle, framing, lighting ("85mm portrait lens, shallow depth of field, golden-hour window light"). Vocabulary: [references/style-guide.md](references/style-guide.md).
- **Text in images.** Quote the exact words, describe the font ("bold condensed sans-serif"), placement and hierarchy. Use `pro` for anything beyond a short headline. Finalize the copy before generating; rewrite it yourself rather than asking the model to invent it.
- **Complex scenes step by step.** "First, a misty forest at dawn. In the foreground, a moss-covered stone altar. On the altar, a single glowing sword."
- **Stickers and icons.** Name the style, outline weight and shading, and ask for a plain white background.
- **Edits.** "Using the provided image, change only X to Y. Keep A, B and C exactly as they are." Describe critical details (a face, a logo) you need preserved.
- **References.** Say what to take from each image. nb2 holds fidelity for up to about 10 objects and 4 characters; `pro` up to 6 objects, 5 characters and 3 style references (14 images total either way).

Examples:

```text
generate_image(model: "nb2", aspectRatio: "1:1", prompt:
"A studio product photograph of a matte black ceramic coffee mug on polished concrete.
Three-point softbox lighting gives soft highlights and no harsh shadows. Slightly elevated
45-degree angle, sharp focus on the steam rising from the coffee. Minimal, premium e-commerce look.")

generate_image(model: "pro", aspectRatio: "2:3", imageSize: "2K", prompt:
"A retro 1970s travel poster for the Dolomites. Bold headline \"DOLOMITI\" in cream condensed
sans-serif across the top, smaller line \"Summer 2027\" below it. Flat screen-print style,
limited palette of burnt orange, teal and cream, jagged peaks at sunset, a cable car crossing.")

edit_image(image: "gemini-media://files/living-room.png", prompt:
"Change only the blue sofa into a vintage brown leather chesterfield. Keep the pillows,
the rest of the room, the camera angle and the lighting unchanged.")

generate_image(referenceImages: ["dress.png", "model.png"], aspectRatio: "4:5", prompt:
"Professional e-commerce fashion photo: the woman from image 2 wears the blue floral dress
from image 1. Full-body shot outdoors, lighting and shadows matched to a sunny terrace.")
```

More templates (infographics with search, icons, object removal, relighting, outpainting, character consistency): [references/prompt-patterns.md](references/prompt-patterns.md).

## Cost and approvals

- Every call is billed; `count`, `imageSize` and model multiply the price. Draft with `nb2` at 1K (or `nb2-lite` for large batches) and upscale only the keeper.
- Before expensive requests (any 4K, `pro` with `count` > 1, batches of more than four images, many large references) call `gemini-media:estimate_cost` with `mediaType: "image"`, `model`, `imageSize`, `count`, `inputImages` and `compare: true`, then tell the user the figure. Estimates exclude any Google Search grounding fee.
- An error starting with `[confirmation]` means the call is above the user's approval threshold. Show the user the amount, and only after they agree retry the same call with `approvedCostUsd` set to that amount. Never invent approval.
- `[budget]` means a spending cap was reached: stop, tell the user, and show `gemini-media:get_usage`. Do not retry.

## Errors

Tool errors read `[kind] message` followed by `Hint: ...`. Follow the hint; in short:

| Kind | Do |
|------|----|
| `invalid` | Fix the argument named in the message (unsupported ratio or size for the model, too many references, bad path). `gemini-media:list_models` with `detail: true` shows valid values. For a path "outside the allowed directories", pass the image as a `data:` URI, copy it into the server's output directory if you share its filesystem, or ask the user to allow that folder (`gemini-media:get_config` shows the input policy). |
| `safety` | Rephrase: drop real people's names, brands, copyrighted characters, violence. Do not resend the same prompt. |
| `quota`, `unavailable` | The server already retried with backoff. Wait briefly, retry once with a smaller `count` or size, then report. |
| `timeout` | Not retried by the server: the generation may still finish and be billed, so a retry can double-charge. Retry once at most, preferably at a smaller size or with `nb2`, and mention it to the user. |
| `not_found` | Model retired or not on this backend, or a missing input file: check `gemini-media:list_models` (`live: true`) or the path. |
| `auth`, `permission` | Stop. Ask the user to run `gemini-media-mcp doctor` and check the API key, billing or Vertex AI access. |
| `budget`, `confirmation` | See Cost and approvals. |

Never loop on an error that costs money; one retry at most.

## Interaction mode

- **With a user present:** if purpose, text or ratio is unclear and matters, ask at most two short questions, or state your assumptions and generate. After each result, offer two or three concrete next steps (a specific edit, a `pro` final, animating it), not a long menu.
- **Autonomous (nobody to ask):** do not block on questions. Choose sensible defaults (`nb2`, 1K, ratio from the destination), stay below the confirmation threshold (never set `approvedCostUsd` yourself), review your own output and make at most two corrective edits. Finish with a short report: prompt used, model, parameters, file paths, total cost.

## Chaining

- Animate a still or build keyframes for video: **gemini-video** (pass the image `uri` as `image`, `lastFrame` or `referenceImages`; generate keyframes at 16:9 or 9:16 to match the clip).
- Album art or cover for a track: **gemini-music** (images can also inspire the music).
- Multi-asset projects (promo, ad, story video with voiceover and music): **gemini-media-production**.

## References

- [references/prompt-patterns.md](references/prompt-patterns.md): copy-ready templates for generation and editing recipes.
- [references/style-guide.md](references/style-guide.md): photography, lighting, art-style vocabulary, and exact pixel sizes per aspect ratio.
