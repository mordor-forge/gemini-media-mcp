# Prompt patterns

Copy-ready templates for `gemini-media:generate_image` and `gemini-media:edit_image`. Replace the bracketed parts and keep the sentence form. Each pattern lists the parameters worth setting.

## Contents

- Generation: [photoreal scene](#photoreal-scene), [product shot](#product-shot), [portrait](#portrait), [text-heavy poster or logo](#text-heavy-poster-or-logo), [infographic with live data](#infographic-with-live-data), [minimalist background](#minimalist-background-for-overlays), [sticker or icon](#sticker-or-icon), [storyboard or comic](#storyboard-or-comic-panels), [sketch to finished image](#sketch-to-finished-image)
- Editing: [add an element](#add-an-element), [remove an object](#remove-an-object), [replace one thing](#replace-one-thing-semantic-inpainting), [style transfer](#style-transfer), [relight](#relight), [outpaint](#outpaint-change-the-aspect-ratio), [fix text](#fix-or-replace-text)
- Multiple images: [composite and try-on](#composite-product-placement-and-try-on), [logo placement](#logo-or-detail-placement), [character consistency](#character-consistency), [variations](#variations-and-ab-options)
- [Model notes and limits](#model-notes-and-limits)

## Generation

### Photoreal scene

```text
A photo of [subject with specific details] [doing action] in [place and time].
[Composition: shot type and angle]. [Lighting]. Captured with [lens], [depth of field].
The mood is [mood]. [Aspect or orientation cue if it matters].
```

Parameters: `aspectRatio` from the destination; `nb2`, 1K for drafts.

### Product shot

```text
A high-resolution, studio-lit product photograph of [product: material, color, finish]
on [surface]. [Lighting setup, e.g. three-point softbox with soft diffused highlights].
[Camera angle, e.g. slightly elevated 45-degree shot] to show [key feature].
Sharp focus on [detail]. [Background: seamless off-white / lifestyle setting].
```

Pass a photo of the real product in `referenceImages` and say "the product from image 1, exactly as it looks" to keep branding and shape. Use `pro` for small printed labels.

### Portrait

```text
A close-up portrait of [person: age, features, expression, clothing] [activity],
in [setting]. [Light source and direction] highlights [texture]. 85mm portrait lens,
soft blurred background. The mood is [mood]. Vertical portrait orientation.
```

Parameters: `aspectRatio: "4:5"` or `"2:3"`. Generic people only; real, identifiable people are likely to be refused.

### Text-heavy poster or logo

```text
A [poster / logo / menu / label] for [brand or event], [audience and purpose].
Headline "[EXACT TEXT]" in [font description] at [position]; secondary line "[exact text]"
[size and position]. [Layout and hierarchy]. [Palette]. [Illustration or background].
```

Parameters: `model: "pro"`, `imageSize: "2K"` for finals. Keep each text element short and quote it exactly; write the final copy yourself first. Check spelling in the result and fix with an edit (see [fix text](#fix-or-replace-text)).

### Infographic with live data

```text
Create a clean, modern infographic about [topic, with time frame, e.g. "this week's
weather forecast for Milan"]. Use current data. Title "[TITLE]". [Chart or layout type].
[Icon style]. [Palette]. Clear labels, large readable numbers.
```

Parameters: `model: "pro"`, `googleSearch: true`, `aspectRatio: "9:16"` or `"4:5"` for mobile. `googleSearch` also works on `nb2` with the Gemini API (not on Vertex AI, where it is dropped with a warning), and not on `nb2-lite`. Verify figures in the output against a source before publishing.

### Minimalist background for overlays

```text
A minimalist composition featuring [single subject] positioned in the [corner/third]
of the frame. The background is a vast, empty [color/texture] canvas, creating
significant negative space for text. Soft, diffused lighting from the [direction].
```

### Sticker or icon

```text
A [kawaii / flat vector / 3D clay] style [sticker / app icon] of [subject] [pose or action].
Bold, clean outlines, [cel / flat / soft gradient] shading, [palette].
The background must be plain white.
```

Parameters: `aspectRatio: "1:1"`, `imageSize: "512"` or `"1K"`. For an icon set, generate the first icon, then pass it as a reference for the rest: "Same style, stroke weight and palette as image 1, now showing [next subject]".

### Storyboard or comic panels

```text
Make a [3/4]-panel [comic / storyboard] in [art style]. Panel 1: [action].
Panel 2: [action]. Panel 3: [action]. [Speech bubble text in quotes, if any].
Keep the character from image 1 consistent in every panel.
```

Parameters: character reference in `referenceImages`; `pro` when there is dialogue text.

### Sketch to finished image

```text
Turn this rough [pencil sketch / wireframe / napkin drawing] of [subject] into
[a polished photo / a finished illustration] of [final description]. Keep [the lines,
proportions, layout] from the sketch; add [materials, colors, lighting].
```

Pass the sketch as `referenceImages[0]` in `generate_image`, or as `image` in `edit_image`.

## Editing

All edits use `gemini-media:edit_image` with `image` set to a path or `uri`. The original file is never overwritten.

### Add an element

```text
Using the provided image, add [object] [where]. Match the existing lighting,
perspective and style so it looks like it was always there. Keep everything else unchanged.
```

### Remove an object

```text
Remove [object] from [location in the image]. Fill the area naturally with
[what should be behind it]. Keep all other elements, colors and lighting unchanged.
```

### Replace one thing (semantic inpainting)

```text
Change only the [object] to [new object with details]. Keep the rest of the scene,
including [nearby elements] and the lighting, unchanged.
```

### Style transfer

```text
Transform this photograph into the style of [style description: medium, brushwork,
palette]. Preserve the original composition of [key elements], but render everything
with [style traits].
```

To match another image's style instead, add it to `referenceImages`: "Restyle image 1 to match the palette, brushwork and lighting of image 2."

### Relight

```text
Relight this scene as [time of day / light setup, e.g. warm golden hour from the left,
long soft shadows]. Keep the subject, pose, composition and colors of objects unchanged;
only the lighting and shadows change.
```

### Outpaint (change the aspect ratio)

Set `aspectRatio` to the new ratio (e.g. a 1:1 image to `"16:9"`) and describe what fills the new space:

```text
Extend the scene to the [left and right]: continue [the beach, the skyline] naturally,
matching perspective and lighting. Keep the original subject and framing unchanged in the center.
```

### Fix or replace text

```text
Replace the text "[old text]" on the [sign / label] with "[NEW TEXT]", same font,
size, color and perspective. Change nothing else.
```

Use `model: "pro"` for text fixes on images made by other models.

## Multiple images

### Composite, product placement and try-on

```text
Create a [professional e-commerce / lifestyle] photo. Take the [item] from image 1 and
[place it on / have the person in image 2 wear it]. [Shot type]. Adjust lighting and
shadows to match [environment].
```

Name each input by its position and say what to take from it. Put the image whose framing or ratio you want first (the default ratio follows the first reference).

### Logo or detail placement

```text
Take the person in image 1 (brown hair, blue eyes, neutral expression). Add the logo
from image 2 onto the black t-shirt so it looks printed on the fabric and follows its folds.
Keep the face and features completely unchanged.
```

### Character consistency

1. Create a character sheet: "Full-body character reference of [character bible: age, build, face, hair, outfit, colors], front view, neutral pose, plain light-gray background." Save the `uri`.
2. For every new scene, pass the sheet in `referenceImages` and repeat the same character-bible sentence: "The character from image 1, identical face, hair and outfit, now [action] in [scene]."
3. For new angles, generate one view at a time and add previous good views as extra references ("in profile looking right", then "three-quarter view").
4. Include a pose reference image for complex poses.

`nb2` keeps up to four characters consistent in one image; `pro` up to five.

### Variations and A/B options

Use `count: 2-4` with one prompt for close variations, or separate calls with a single changed variable (palette, angle, headline) for controlled A/B options. Label them with `outputName` (e.g. `ad-a`, `ad-b`).

## Model notes and limits

- Best prompt languages: English, ar-EG, de-DE, es-MX, fr-FR, hi-IN, id-ID, it-IT, ja-JP, ko-KR, pt-BR, ru-RU, ua-UA, vi-VN, zh-CN.
- Reference fidelity: `nb2` up to about 10 objects and 4 characters; `pro` up to 6 objects, 5 characters and 3 style references; 14 images total. `nb2-lite` handles object references but not character consistency.
- Search grounding does not use real-world images of people.
- All generated images carry an invisible SynthID watermark.
- The models reason before rendering; complex prompts take longer, and `pro` at 4K can take a minute or more.
