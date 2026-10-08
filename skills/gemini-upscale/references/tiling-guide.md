# Tiling guide

How `tile_image` and `stitch_tiles` work, how to pick settings, and a complete worked example.

## Contents

- [How the tiles are cut](#how-the-tiles-are-cut)
- [Picking grid, padding and size](#picking-grid-padding-and-size)
- [How stitching works](#how-stitching-works)
- [Worked example: a 1024 px portrait to 8K](#worked-example-a-1024-px-portrait-to-8k)
- [Troubleshooting](#troubleshooting)

## How the tiles are cut

- **Grid (pass 1):**
  - `grid: 3` splits the image into 3 x 3 cells.
  - Each cell is padded by `padding` (20% of the cell) on every side that is not on the image border.
  - The padded cell then **grows to the nearest aspect ratio the edit model supports**: Nano Banana 2.1 has 14 ratios, from 1:8 to 8:1. If growing would leave the image, the crop shrinks instead, as long as the cell stays covered.
  - Because the crop already has a supported shape, the model returns it as a pure scale-up: no cropping, letterboxing or stretching to undo.
- **Regions (later passes):** each region is padded and grown the same way. Overlapping regions are fine.
- **Crop size:** crops are saved as lossless PNG, at most 2048 px on the long side. The model samples its inputs at about 1K, so bigger crops would only add upload time.
- **Reference (first pass only):** the `reference` file is the original at most 2048 px. It goes with every pass-1 tile so the model knows the identity, colors and light. Refinement passes send the crop alone, because next to a close-up the model tends to redraw the whole reference photo instead.

Keep the tile's `aspectRatio` in the `edit_image` call. A wrong ratio is the most common reason for a rejected tile.

## Picking grid, padding and size

| Situation | Setting |
|-----------|---------|
| Normal photo to 8K | `grid: 3`, `padding: 0.2`, `longEdge: 8192` (defaults) |
| Output beyond ~9K, or a panorama | `grid: 4` (16 tiles, about $2 per pass on nb2) |
| Very small source (under ~600 px) | Consider `longEdge: 4096`. Nearly all detail will be invented, so say so. |
| Busy scene with many small subjects | `grid: 4`: smaller tiles keep subjects coherent |
| Tiles disagree at seams (different textures) | Raise `padding` to 0.3 so each tile sees more context |

`tile_image` reports `nativeLongEdge`: about how many pixels of model-made detail the long edge carries when every tile comes back at 4K.

- On a 3 x 3 grid this is about 8,700-11,000 px, so an 8K export is backed by real model output.
- If `longEdge` is above `nativeLongEdge`, the remainder is interpolated. Use a finer grid, or tell the user.

## How stitching works

1. **Align.** Each tile is compared with the image it was cut from, at that image's own resolution, where both carry the same structure. The server searches for the best scale (independently in x and y) and shift. A tile is rejected when:
   - its shape differs from the crop;
   - its structure matches poorly (`match` < 0.5);
   - it is off by more than 10% or zoomed by more than 8%.
2. **Color.** A smooth per-channel offset, at about 1/8 of the tile in resolution, moves the tile's broad exposure and color onto the image's. Fine texture is never copied from the old pixels. Turn it off with `colorMatch: false` if the user wants the model's grading.
3. **Blend.** Tiles are averaged with smooth weights that fade toward their inner edges.
   - The fade is at most half the overlap with each neighbor, so every overlap has at least one fully opaque tile and the old image never shows through a seam.
   - Sides on the image border are not faded.
   - The base image (the interpolated original, or the previous pass) keeps only the weight the tiles leave. It shows through only where no tile was placed.
4. **Output.** The result is a lossless PNG at the planned size, with a downscaled preview and 100% crops for inspection. An 8K stitch takes 10-30 s and up to about 1 GB of RAM.

## Worked example: a 1024 px portrait to 8K

```text
tile_image(image: "~/Pictures/portrait.jpg")
  -> 9 tiles (pass 1, grid); output 8192x5461; ~10,700 px native
     reference gemini-media://files/portrait-reference.png
     tile 1 r1c1 (aspectRatio 3:2) gemini-media://files/portrait-p1-t1-r1c1.png ...
     editing every tile once with gemini-nano-banana-2.1 at 4K: ~$1.12

# Tell the user: pass 1 ~$1.12, pass 2 ~$0.50-1.00, total about $1.60-2.10. Approved.

edit_image(image: ".../portrait-p1-t1-r1c1.png", referenceImages: [".../portrait-reference.png"],
           aspectRatio: "3:2", imageSize: "4K", model: "nb2",
           prompt: "<tile prompt> + Hair clause + Foliage clause")
... same for tiles 2-9 (tile 5, the face: + Skin and Eyes clauses)

stitch_tiles(job: ".../portrait-p1-tiles.json",
             tiles: [{tile: 1, image: ".../portrait-p1-t1-r1c1-edit.png"}, ... 9 entries])
  -> portrait-upscaled.png 8192x5461; 9 placed (match 0.86-0.95)

# Inspect preview and corner details. Choose pass-2 regions from the preview:
tile_image(image: "gemini-media://files/portrait-upscaled.png", regions: [
  {x: 0.42, y: 0.12, width: 0.22, height: 0.30, label: "face"},
  {x: 0.30, y: 0.05, width: 0.45, height: 0.35, label: "hair"},
  {x: 0.35, y: 0.45, width: 0.40, height: 0.35, label: "shoulder"},
  {x: 0.05, y: 0.55, width: 0.25, height: 0.30, label: "hand"}])
  -> 4 tiles (pass 2, regions); output keeps 8192x5461; no reference on refinement passes

edit_image(image: ".../portrait-p2-t1-face.png", aspectRatio: <tile's>, imageSize: "4K",
           prompt: "<refinement prompt> + Skin and Eyes clauses")   # no referenceImages
... x4, stitch_tiles(job: ".../portrait-p2-tiles.json", tiles: [...])
  -> portrait-upscaled-p2.png
```

Deliver `portrait-upscaled-p2.png` with its size, about 10,700 px of model detail, 13 tiles and the total cost. Describe the added detail as AI-reconstructed.

## Troubleshooting

| Symptom | Cause and fix |
|---------|---------------|
| A tile is rejected with "different shape" | `aspectRatio` was missing or wrong in `edit_image`. Retry with the tile's ratio. |
| A tile is rejected with "does not match" | The model reframed, zoomed or redrew the crop. Retry with the field-of-view sentence first in the prompt (see the skill). With `pro`, try `nb2`. |
| Every refinement tile is rejected and the edits show the whole photo | `referenceImages` was sent on a refinement pass. Edit each crop alone. |
| A refinement region comes back no sharper | The region is too large (`tile_image` warned). Use regions about a third of the image's long side or smaller. |
| Doubled edges in a detail crop | Local drift inside a tile (the note "part of the tile differs"). Retry that tile; if it persists, cover the area with a smaller region in the next pass. |
| Color steps between tiles | `colorMatch` was off, or a tile changed the lighting. Keep `colorMatch` on and stress "no relighting". |
| Two irises differ | The eyes were in different tiles or regions. Re-render one region holding both eyes in the next pass. |
| Plastic or over-smooth skin | Remove beautifying words and add the Skin clause. Prefer `nb2` at 4K. |
| Repeating texture in grass, hair or fabric | Add the matching clause ("no repeated patterns"), raise `padding`. |
