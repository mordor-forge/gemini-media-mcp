---
name: gemini-upscale
description: Upscales photos past the 4K limit of a single edit (8K by default) with the gemini-media MCP server. It cuts the image into overlapping crops, re-renders each at 4K with Nano Banana 2.1 using the whole photo as context, aligns and blends them into one large PNG, then optionally re-renders a few regions to fix defects such as mismatched eyes. Use it when the user wants to upscale, enlarge or add detail to a photo for print, wallpapers or large screens, even if they never mention Gemini. Limits - it invents plausible detail rather than recovering the original, so faces, eyes, small text and textures can change; it is unsuitable for forensic, archival, evidence or product-label work; a run costs about $1.10-1.90 on nb2 at 8K and takes a few minutes.
license: Apache-2.0
compatibility: Requires the gemini-media MCP server v1.1 or later (tile_image and stitch_tiles tools) with a Gemini API key or Vertex AI project.
metadata:
  author: mordor-forge
  version: "1.0.0"
  mcp-server: gemini-media
---

# Gemini Upscale (tiled, past 4K)

A single `edit_image` call tops out at 4K. This skill goes further. It cuts the photo into overlapping tiles, has the model re-render each tile at 4K with the full photo as context, and stitches the tiles into one 8K image. That single pass already fills every pixel of an 8K output with model-rendered detail. A second pass re-renders a few regions, and only to fix visible defects.

**What it is and isn't.** This is generative enhancement. The model draws plausible pores, hair strands, fabric weave and foliage that were never in the file. Expect a sharper, more detailed image that is not a faithful record:

- eye color, small text, logos, jewelry and fine patterns can drift;
- a closer region pass can make identity drift worse.

Say this plainly to the user. Refuse or warn for evidence, forensics, archival restoration, documents and product labels.

## Quick start

1. **Choices.** If the user has not said, ask once: "Output 8K (8192 px long edge) or another size?" Default to `nb2` (Nano Banana 2.1). `pro` costs about twice as much per tile.
2. **Plan pass 1.** Call `gemini-media:tile_image` with `image` (the photo) and `longEdge` if not 8192. The result lists the tiles, a `reference` image (first pass only), the `job`, and the cost of editing every tile.
3. **Budget.** Estimate the whole run before spending anything:
   - pass 1 costs what `tile_image` reports;
   - allow one or two retries and an optional fix pass of 1-4 regions, each at the same per-tile price.

   Tell the user the total and get approval (see Cost and approvals).
4. **Edit every tile** with `gemini-media:edit_image`, issuing the calls in parallel if your client can:
   - `image`: the tile's `crop.uri`;
   - `referenceImages`: `[reference.uri]` on the first pass only;
   - `aspectRatio`: the tile's `aspectRatio`;
   - `imageSize`: the plan's `imageSize` (4K);
   - `model`: the plan's `model`;
   - `prompt`: the tile prompt below (the refinement prompt on later passes).
5. **Stitch.** Call `gemini-media:stitch_tiles` with the `job` and `tiles: [{tile, image: <edit result uri>}, ...]`. Read the report:
   - retry rejected tiles at most twice each;
   - inspect the preview and the 100% detail crops.
6. **Fix pass (only for visible defects).** Run it if the preview or the details show something wrong, such as two different iris colors or garbled hands, teeth or text. Call `tile_image` with `image` set to the stitched `file.uri` and 1-4 `regions` around the defects (Choosing regions below). Edit each crop **on its own, without `referenceImages`**, using the refinement prompt, then stitch again. The server carries the original and the output size over from pass 1. At 8K this pass replaces content but adds no resolution, and `tile_image` says so.
7. **More passes add resolution only** when pass 1 did not fill the output, that is when you see the warning "interpolated beyond about N px" (for example `longEdge` 16384, or tiles below 4K). Then use 4-8 tight regions.
8. **Deliver** (Delivering below).

## Tools

The tools live on the `gemini-media` MCP server. Harnesses name MCP tools differently (`mcp__gemini-media__tile_image`, `gemini-media.tile_image`, or plain `tile_image`); match on the part after the server name.

| Tool | Role | Cost |
|------|------|------|
| `gemini-media:tile_image` | Cuts a grid (pass 1) or regions (later passes) into crops shaped to the model's supported ratios. Saves the crops, a job file and, on the first pass, a reference copy of the original (at most 2048 px). Warns when a region is too large to gain detail. | Free, local |
| `gemini-media:edit_image` | Re-renders one crop at 4K. | ~$0.12 per tile on nb2, ~$0.25 on pro |
| `gemini-media:stitch_tiles` | Aligns each tile with the image, corrects scale and shift, matches broad color, blends overlaps and saves a PNG. Reports placed and rejected tiles and returns previews. | Free, local, 10-30 s at 8K |
| `gemini-media:get_usage` | Spend so far, budget left. | Free |

Tiles that are rejected or left out keep the underlying pixels: the interpolated original in pass 1, the previous pass's result later. Nothing turns black or blank.

## The tile prompt (pass 1)

The server sends `image` first and the references second. So **image 1 is the crop to re-render and image 2 is the full original**. Use this text and append only the clauses that match what the crop shows:

```text
Image 1 is a crop of the photo in image 2. Re-render image 1 as a sharp, high-resolution
photograph of exactly the same crop: same framing, edges, composition and geometry; nothing
added, removed, moved, re-centered or zoomed. Image 2 is the authority for identity, anatomy,
color and lighting. Reconstruct plausible photographic detail at this resolution while keeping
expression, pose, contours, clothing, genuine imperfections and the original focus falloff
(out-of-focus areas stay soft). Clean up compression artifacts and noise. Avoid invented
objects, marks or text, beautification, relighting, halos, ringing, over-sharpening,
embossed, crosshatched or repeating texture and synthetic grain.
```

Clauses, each added only when that material is visible in the crop:

- **Skin:** "Skin shows irregular pores and fine creases with quiet, smooth intervals; no airbrushing, no new blemishes or freckles."
- **Hair:** "Hair forms cohesive strand groups with a few selective flyaway fibers and a natural, sparse hairline; no uniform combed texture."
- **Eyes:** "Keep the exact iris color of image 2, with irregular radial fibers, a round coherent pupil and small, restrained catchlights."
- **Teeth:** "Keep the teeth exactly as shown: same count, shape, spacing and shade; natural gums and lips."
- **Textiles:** "Keep the original weave direction, spacing and motifs."
- **Text and logos:** "Keep exactly the letters visible in image 2; where they are unreadable, keep them soft rather than inventing letters."
- **Foliage, water, rock:** "Natural irregular detail; no repeated patterns."
- **Sky or plain backgrounds:** "Keep it smooth and clean; do not add texture."

The same prompt goes to every tile in a pass, except for the material clauses. Keep the wording identical across tiles so neighbors render alike. More guidance and a worked example: [references/tiling-guide.md](references/tiling-guide.md).

## The refinement prompt (later passes)

On refinement passes, send the crop alone. The crop already carries the identity, colors and light from pass 1. In live tests, sending a whole-photo reference next to a close-up made the model redraw the whole photo in 4 of 5 tiles, and `stitch_tiles` rejected them all. Use this text plus the same clauses, with "image 2" replaced by a description: for example "Both eyes have the same dark brown iris color", or "Keep exactly the letters shown".

```text
Re-render this photo crop as a sharp, high-resolution photograph of exactly the same crop:
same framing, edges, composition and geometry; nothing added, removed, moved, re-centered or
zoomed. Keep the person's identity, anatomy, expression, color and lighting exactly.
Reconstruct plausible photographic detail at this resolution while keeping genuine
imperfections and the original focus falloff. Avoid invented objects or marks,
beautification, relighting, halos, ringing, over-sharpening, embossed, crosshatched or
repeating texture and synthetic grain.
```

## Choosing regions (later passes)

Look at the stitched preview, then pass `regions` as fractions of the image (`x`, `y`, `width`, `height` from the top-left, 0-1) with a short `label`:

- **Fix pass (the usual case at 8K):** pick regions around defects only, such as both eyes when their colors differ, a garbled hand, misshapen teeth or a broken line of text.
  - Pass 1 with 4K tiles already renders about 10,000 px of detail for an 8,192 px output.
  - In testing, four tight regions re-rendered at 2.4x came back no sharper than pass 1.
  - Re-rendering an area that looks fine only spends money and risks drift.
- **Detail pass (only after an "interpolated beyond" warning):** pick 4-8 high-information regions, such as faces, hands, hair masses, textured clothing or signage.
- **Skip low-information areas:** sky, blurred background and plain walls only waste money.
- **Matching features:** put both eyes in one region rather than one per eye, so the irises are re-rendered together and stay alike. Do the same for earrings and pairs of hands.
- **Size:** keep each region to about a third of the image's long side or less. The model sees each crop at 2048 px at most, so in a detail pass a larger region comes back no sharper. `tile_image` warns about such regions. A fix region can be larger when the defect needs it, for example both eyes.
- **Padding:** `tile_image` adds 20% context around each region and grows it to a supported aspect ratio. Draw regions tight around the feature.
- **Without image previews:** if your client cannot show images, you cannot choose regions. Stop after pass 1, or ask the user where the important details are.

## Reading the stitch report

| Field | Meaning | Do |
|-------|---------|----|
| `placed`, `match` 0.8-1 | Tile aligned. `shift`/`scale` show the correction applied. | Nothing. |
| `placed` with a note "part of the tile differs" | One area of the tile changed structure, for example a moved hand or an invented object. | Look at that tile in a detail crop; retry it if the change is visible. |
| `rejected` | Different framing or shape, or content that does not match the photo. The area keeps the prior pixels. | Retry at most twice, with the same inputs and this sentence first: "Keep exactly the field of view of image 1: its edges must cut through <what lies at its edges> at the same places. Do not zoom out and do not reveal anything outside image 1." (In testing this fixed a tile the model had zoomed out.) If it keeps failing, deliver without it and say so. |
| `unedited` | You did not pass that tile. | Intended only when the area needs no detail. |
| Warning "interpolated beyond about N px" | The tiles carry less detail than the output size. | Tell the user, or rerun with `grid: 4` or a smaller `longEdge`. |

After retrying, call `stitch_tiles` again with **every** tile, the retried ones included. Each call rebuilds the image from scratch.

Inspect the whole-image preview and the 100% crops (grid corners in pass 1, region centers later). Look for:

- doubled edges;
- visible seams or color steps between tiles;
- drift between matching features (two different iris colors);
- repeating texture.

Use `details` to ask for 100% crops anywhere else. At most 2 targeted retries per defect.

## Cost and approvals

- `tile_image` returns the cost of editing every tile once. Regions in a fix pass cost the same per tile. Example on nb2 at 4K, about $0.12 per tile:

  | Run at 8K | Total |
  |-----------|-------|
  | Pass 1 (9 tiles) with one or two retries | about $1.10-1.40 |
  | Optional fix pass, 1-4 regions | about $0.12-0.50 more |

  `pro` is about twice that, and `grid: 4` (16 tiles) about $2 for pass 1.
- Tell the user the total before the first edit. One approval covers the run. If an `edit_image` call still fails with `[confirmation]`, retry it with `approvedCostUsd` set to that call's quoted amount; the run approval covers it. Never set `approvedCostUsd` without the user's approval of the run.
- `[budget]` means a cap was hit mid-run. Stop and report how many tiles are done. The job file and finished edits stay on disk: once the budget allows, edit the remaining tiles and stitch with all of them.
- Do not "test" on a few tiles and then redo them all. Tiles are independent, so every finished tile counts.

## Errors

Tool errors read `[kind] message` followed by `Hint: ...`. Follow the hint.

| Kind | Do |
|------|----|
| `invalid` from `tile_image` | Fix the argument: `grid` 2-4, `padding` 0.05-0.5, regions inside the image, `longEdge` 1024-16384 and at most 70 MP. |
| `invalid` from `stitch_tiles` | Use the `job` uri from `tile_image` and tile numbers from its list. If the tiled image changed, run `tile_image` again. |
| `safety` on a tile | Usually a close crop of a person. Retry once with a neutral prompt (drop adjectives about the body); if it repeats, leave that tile out and say so. |
| `quota`, `unavailable` | Send fewer edits in parallel and retry once. |
| `timeout` | The edit may still be billed. Retry once at most. |
| `auth`, `permission`, `budget` | Stop and tell the user (`gemini-media-mcp doctor` for credentials). |

Never loop on an error that costs money.

## Delivering

Report:

- the file path and `uri`;
- the pixel size;
- how much of it is model-rendered detail (`nativeLongEdge`) versus interpolation;
- the passes run and the tiles placed or rejected per pass;
- the total cost (`gemini-media:get_usage` or the sum of the edits);
- the prompts used.

Call the added detail "reconstructed" or "AI-generated", never "recovered". Each tile Google returns carries its SynthID watermark.

## Interaction mode

- **With a user present:** ask the size question once if unspecified and state the cost. Show the pass-1 result and propose a fix pass only for defects you can name.
- **Autonomous:** run one pass at 8K on nb2, plus a fix pass of at most 4 regions only for clear defects. Proceed only if `get_usage` shows the budget covers the estimate. Never set `approvedCostUsd` yourself. Retry at most twice per tile. Finish with the delivery report, including rejected tiles.

## Chaining

- Generate or fix the image first with **gemini-image**, and upscale only the final version.
- For posters and prints with text, render the text at 4K with **gemini-image** (`pro`) rather than relying on the upscale to invent legible lettering.
