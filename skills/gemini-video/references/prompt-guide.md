# Video prompt guide (Veo and Omni)

Vocabulary and recipes for `gemini-media:generate_video`, `gemini-media:extend_video` and `gemini-media:edit_video`. Write prompts as a short, concrete paragraph; the terms below are building blocks, not keyword lists.

## Contents

- [Prompt formula](#prompt-formula)
- [Shot types and framing](#shot-types-and-framing)
- [Camera movement](#camera-movement)
- [Lens, focus and speed](#lens-focus-and-speed)
- [Lighting and color](#lighting-and-color)
- [Styles](#styles)
- [Audio cues](#audio-cues)
- [Recipes](#recipes): text-to-video, animate a still, first and last frame, ingredients, extension, dialogue scene, product shot, loop
- [Gemini Omni](#gemini-omni-omni): differences from Veo, editing, extension
- [Negative prompts](#negative-prompts)
- [Troubleshooting](#troubleshooting)

## Prompt formula

```text
[Shot type and angle] of [subject with details] [action, in time order] in [context: place,
time, weather]. [Camera movement]. [Lighting and color]. [Style]. [Audio: dialogue, sound
effects, ambience, music].
```

Google's prompt guide centers on subject, context, action, style, camera motion, composition and ambiance. More specific detail gives more control: compare "a man on a phone" with "a close-up cinematic shot follows a desperate man in a weathered green trench coat as he dials a rotary phone on a gritty brick wall, bathed in the glow of a green neon sign; the camera dollies in; shallow depth of field".

## Shot types and framing

| Term | Effect |
|------|--------|
| Extreme wide / establishing shot | Place and scale; subject small in frame |
| Wide / full shot | Whole body plus surroundings |
| Medium shot / medium close-up | Waist or chest up; conversation, presenting |
| Close-up / extreme close-up | Face, hands, product detail; emotion |
| Over-the-shoulder | Dialogue between two people |
| POV | Through the subject's eyes ("POV from a vintage car driving in the rain") |
| Low angle / high angle | Power / vulnerability |
| Bird's-eye / top-down | Patterns, maps, food, desks |
| Dutch angle | Unease, tension |
| Two-shot | Two characters in frame together |

Composition cues: centered symmetry, rule of thirds, leading lines, foreground framing (through a doorway or leaves), negative space for titles.

## Camera movement

| Move | Prompt phrasing |
|------|-----------------|
| Static | "locked-off static shot", "tripod shot" |
| Pan | "slow pan left across the skyline" |
| Tilt | "tilt up from the boots to the face" |
| Dolly in / out | "slow dolly in toward her eyes", "dolly out to reveal the crowd" |
| Push-in | "gentle push-in on the product" |
| Truck / lateral track | "camera trucks right alongside the runner" |
| Tracking / follow | "tracking shot following the cyclist from behind" |
| Orbit / arc | "the camera orbits 180 degrees around the sculpture" |
| Crane / jib | "crane up from street level to reveal the city" |
| Aerial / drone | "aerial drone flyover of the coastline", "FPV drone dives down the waterfall" |
| Handheld | "handheld, slightly shaky documentary feel" |
| Gimbal / Steadicam | "smooth gimbal shot gliding through the market" |
| Zoom | "slow zoom in" (lens zoom, flatter than a dolly) |
| Dolly zoom | "dolly zoom (vertigo effect) on his shocked face" |
| Whip pan | "whip pan to the door as it bursts open" |
| Rack focus | "rack focus from the raindrops on the glass to the woman outside" |

Use one main move per shot and say its speed (slow, gentle, rapid).

## Lens, focus and speed

- Lens: wide-angle, ultra-wide, telephoto compression, macro, fisheye, anamorphic with horizontal lens flares
- Focus: shallow depth of field, deep focus, soft focus, bokeh background, rack focus
- Speed: slow motion, real time, time-lapse (clouds, city traffic), hyperlapse, freeze moment
- Frame feel: 24 fps cinematic (Veo outputs 24 fps), film grain, 16mm, VHS, found footage

## Lighting and color

- Golden hour, blue hour, overcast soft light, harsh noon sun, moonlight
- Neon signage, practical lamps, candlelight, firelight, flickering fluorescent
- Volumetric fog, god rays through windows, backlit silhouette, rim light
- Grades: warm muted orange tones, cool blue tones, teal and orange, high contrast black and white, pastel, desaturated bleach bypass

## Styles

- Cinematic realism, nature documentary, news footage, vlog selfie, security camera
- Film noir, 1970s film look, Wes Anderson-like symmetry (describe the traits: centered symmetric framing, pastel palette)
- 3D animated cartoon, claymation, stop-motion, paper cut-out animation, watercolor animation, anime, pixel art
- Surreal, dreamlike, miniature tilt-shift, macro nature

## Audio cues

Veo generates a synchronized soundtrack from the prompt (always on with the Gemini API).

| Cue | How to write it |
|-----|-----------------|
| Dialogue | Speaker label, optional delivery in parentheses, words in quotes: `Man: (hand on his hunting knife) "That's no ordinary bear."` |
| Sound effects | Concrete sources and actions: "rough bark scraping, snapping twigs, footsteps on damp earth" |
| Ambience | Background bed: "a lone bird chirps", "distant city traffic hum", "rain on a tin roof" |
| Music | Genre, instrument, mood: "a soft solo piano score", "upbeat synth-pop underscore" |
| Silence or restraint | "no dialogue", "only natural ambient sound" |

Tips:

- About 8 seconds fits one or two short lines. Long lines get rushed or cut.
- Make the speaker visible and identify who speaks each line; keep one speaker per line.
- A labeled sentence such as `Sound: ...` or `Audio: ...` after the visual description keeps cues from blending into the visuals.
- For voice to carry across an extension, the voice must be present in the last second of the clip being extended.
- If voiceover and music will be added later, request only ambient sound and keep dialogue out of the prompt.

## Recipes

### Text-to-video

```text
A wide shot of a misty Pacific Northwest forest. Two exhausted hikers push through ferns;
the man stops abruptly and stares at a tree. Close-up: fresh, deep claw marks in the bark.
Man: (hand on his hunting knife) "That's no ordinary bear." Woman: (voice tight with fear,
scanning the woods) "Then what is it?" Snapping twigs, footsteps on damp earth, a lone bird chirps.
```

### Animate a still (first frame)

1. Make the frame with **gemini-image** at 16:9 or 9:16 (the clip's ratio).
2. Call `generate_video` with `image` set to its `uri` and a prompt about motion and sound, not appearance:

```text
The camera slowly pans across the sunlit scene as the tiny surfers carve the turquoise
water. Gentle splashing and a faint cheer.
```

### First and last frame (interpolation)

1. Generate the first frame, then generate the last frame by editing or referencing the first so style, lighting and characters match.
2. Call `generate_video` with `image` (first) and `lastFrame` (last), same aspect ratio. The prompt describes the transition:

```text
The ginger cat accelerates along the coastal road in the red convertible, swerves toward the
cliff edge and launches into the air. Engine roar, screeching tires, the cat's startled yowl.
```

Works on `lite`, `fast`, `standard` and `omni`. Transitions that are physically plausible (a camera move, an action, a time-of-day change) interpolate best.

### Ingredients (reference images)

1. Prepare up to 3 clean images of the subjects to preserve: a person or character, a product, a garment. Plain backgrounds help.
2. Call `generate_video` with `model: "fast"` (or `standard`), `referenceImages`, no `image`/`lastFrame`, 8 s:

```text
A silly cartoon of the angler fish from the reference wearing the pink princess costume,
swimming through dark water and waving the wand around. Bubbling water, a playful harp glissando.
```

Name every ingredient in the prompt. Use this mode for consistent characters or products across several shots.

### Extension (longer takes)

1. Generate the base clip with `fast` or `standard` (720p keeps it consistent with the 720p extensions).
2. When it completes, call `extend_video` with its `jobId` and a prompt describing only what happens next:

```text
The paraglider slowly descends toward the flower-covered valley as the wind softens.
```

3. Poll the new `jobId` with `get_video`. Each step continues from the final second of the previous video, adds about 7 s, and returns the combined video (up to 148 s total). Extend within 2 days of generation (`remoteExpiresAt`).

Continuity tips: keep subject descriptions and style words identical across steps; continue the motion already in progress rather than cutting to a new scene; for a new angle or location, generate a separate shot instead.

### Dialogue scene

```text
Medium two-shot in a cozy cafe, warm afternoon light. A barista slides a latte across the
counter to a customer. Barista: (cheerfully) "One oat latte, extra foam." Customer: (smiling)
"You remembered!" Espresso machine hiss, soft chatter, clinking cups.
```

### Product shot

```text
A slow 180-degree orbit around the matte black wireless earbuds case resting on wet slate,
water droplets beading on the surface. Soft top light with a cool rim light, shallow depth of
field, premium commercial look. Subtle low electronic hum and a single water drip.
```

Use the product photo as `image` (starting frame) or as a reference ingredient on `fast` for fidelity.

### Loop

Use the same image as `image` and `lastFrame` and describe a cyclical motion ("the lantern gently sways and returns to rest"). Results vary; check the seam.

## Gemini Omni (`omni`)

Omni follows long, specific prompts closely and renders short text legibly. Differences from Veo:

- **Shots:** by default it builds a small multi-shot sequence. For one shot write "Continuous, unbroken shot of ..."; for your own cuts time the beats: `[0-3s] A person is walking [3-6s] They stop and turn around [6-10s] They start running`.
- **Roles of images:** the server adds `<FIRST_FRAME>` (and `<LAST_FRAME>`) when you pass `image`/`lastFrame`. Refer to reference images as `<IMAGE_REF_0>`, `<IMAGE_REF_1>`... in their order: `in the style of <IMAGE_REF_0> a woman <IMAGE_REF_1> is walking`.
- **Audio:** it always scores the clip. Say what you want: "Sound design: gentle breeze, distant birds. No dialogue." or "upbeat acoustic guitar".
- **Drafts:** iterate at `resolution: "360p"` (faster, about a third of the 720p price), then render the chosen prompt at 720p or above. 1080p and 4K are upscaled.

### Editing with `edit_video`

Short instructions beat descriptions. Name the change and protect the rest:

```text
Add a cat that jumps onto his lap, he begins to pet it. Keep everything else the same.
Make the phone invisible. Keep everything else the same.
Change the season to winter with light snowfall. Keep everything else the same.
```

Omni clips (`jobId` from `omni`) are edited in conversation, so chain small edits instead of one big rewrite. Input files must be 10 s or shorter; to regenerate the soundtrack from scratch, strip the audio from the file first (`ffmpeg -i in.mp4 -an -c:v copy silent.mp4`).

### Extending with `extend_video`

Prompt only what comes next: "The scene continues: the camera pans across the mountains as the music swells." Each call adds up to 10 s, up to 40 s in total. `extend_video` takes no reference images: to bring in a new character or object, start a new `generate_video` with `referenceImages` (and the clip's last frame as `image`).

## Negative prompts

`negativePrompt` takes plain nouns and short noun phrases, not instructions: `"text, subtitles, watermark, logos, blurry faces, extra limbs, background music"`. Do not write "no" or "don't" in it.

## Troubleshooting

| Problem | Try |
|---------|-----|
| Too static | Add explicit action verbs and one camera move with a speed |
| Chaotic motion | One subject, one action, one camera move; shorter clip |
| Character or product drifts | Use `image` or reference ingredients; repeat the same description |
| Unwanted on-screen text or subtitles | `negativePrompt: "text, subtitles, captions, watermark"` |
| Unwanted music or speech | "only natural ambient sound"; `negativePrompt: "music, speech"` |
| Rushed dialogue | Fewer, shorter lines; 8 s duration |
| `filtered` result | Remove real names, brands, copyrighted characters, graphic content; check the input image |
