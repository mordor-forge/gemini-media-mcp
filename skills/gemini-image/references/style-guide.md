# Image style guide

Vocabulary for steering Nano Banana toward a look, plus exact output sizes. Weave these terms into descriptive sentences; do not paste them as keyword lists.

## Contents

- [Photography](#photography): camera and lens, framing and angles, camera and film looks
- [Lighting](#lighting): studio, natural, dramatic
- [Art styles and media](#art-styles-and-media): traditional, digital, movements, contemporary aesthetics
- [Commercial and design styles](#commercial-and-design-styles)
- [Color and mood](#color-and-mood)
- [Aspect ratios and pixel sizes](#aspect-ratios-and-pixel-sizes)

## Photography

### Camera and lens

- **Portrait:** 85mm f/1.8, shallow depth of field, creamy bokeh background
- **Landscape:** 24mm wide-angle, deep focus at f/16, panoramic sweep
- **Macro:** macro lens, extreme close-up, razor-thin focus plane
- **Street:** 35mm prime, candid framing, available light
- **Action:** fast shutter freezing motion, or a GoPro-style ultra-wide with motion blur
- **Fashion editorial:** medium-format camera, controlled studio light
- **Architecture:** tilt-shift lens, corrected verticals, symmetrical composition

### Framing and angles

- Extreme close-up, close-up, medium shot, cowboy shot, full body, wide establishing shot
- Eye level, low angle (heroic), high angle (vulnerable), bird's-eye / top-down, worm's-eye
- Over-the-shoulder, point of view, Dutch angle, isometric 45-degree view
- Rule of thirds, centered symmetry, leading lines, frame within a frame, generous negative space (for text overlays)

### Camera and film looks

- **Fujifilm color science:** warm, film-like tones, flattering skin
- **Leica look:** contrasty, crisp, clinical sharpness
- **Disposable camera:** direct flash, grain, nostalgic snapshot feel
- **Instant film:** soft focus, muted instant-film colors, white border
- **Kodak Portra 400:** warm skin tones, pastel palette
- **Kodak Ektar 100:** vivid saturated colors, fine grain
- **Fujifilm Velvia:** ultra-saturated landscapes
- **Ilford HP5:** classic black and white with pronounced grain
- **CineStill 800T:** tungsten night look with red halation around highlights

## Lighting

### Studio

- Three-point softbox setup (soft, even, commercial)
- Ring light (shadowless, beauty)
- Rembrandt lighting (triangle of light on the shadowed cheek)
- Butterfly / paramount lighting (glamour, shadow under the nose)
- Split lighting (half the face in shadow, dramatic)
- Seamless white or colored paper backdrop, gradient backdrop

### Natural

- Golden hour (warm, long shadows, low sun)
- Blue hour (cool twilight tones)
- Overcast, diffused (soft, even, no hard shadows)
- Harsh midday sun (high contrast, crisp shadows)
- Window light (soft, directional, painterly)
- Dappled light through leaves

### Dramatic

- Chiaroscuro (extreme light-dark contrast)
- Backlight / rim light (glowing outline, silhouette)
- Volumetric fog and god rays
- Neon glow and colored gels (cyberpunk, nightlife)
- Candlelight or firelight (warm, flickering, intimate)
- Practical lights in frame (lamps, screens, signage)

## Art styles and media

### Traditional media

- Oil painting (impasto, glazing), watercolor (wet-on-wet, granulation), gouache, acrylic
- Charcoal, graphite pencil sketch, ink and brush, pastel
- Woodcut, linocut, etching, lithograph, screen print, risograph
- Fresco, mosaic, stained glass, embroidery, paper cut-out collage

### Digital styles

- Digital concept art, matte painting, 3D render (with material and lighting cues)
- Isometric 3D diorama, low poly, voxel art, pixel art (state the resolution feel, e.g. "16-bit")
- Vector illustration, flat design, line art, infographic style
- Claymation look, felt or plush craft look, miniature tilt-shift diorama

### Art movements

- Impressionist, post-impressionist, expressionist, fauvist
- Art nouveau, art deco, Bauhaus, Swiss / International Typographic Style
- Baroque, renaissance, ukiyo-e woodblock, Chinese ink wash
- Pop art, minimalist, brutalist, surrealist, constructivist poster

### Contemporary aesthetics

- Cyberpunk, steampunk, solarpunk, biopunk, retro-futurism
- Dark academia, cottagecore, liminal space, Y2K
- Vaporwave, synthwave / retrowave
- Anime, manga, cel-shaded, chibi, kawaii
- Hand-painted animated-film backgrounds (describe the qualities, not a studio name)

Describe an artist's or studio's qualities (brushwork, palette, subject matter) rather than naming living artists or trademarked franchises; named styles and characters are more likely to be blocked.

## Commercial and design styles

- **Product hero:** seamless backdrop, softbox lighting, crisp reflections, single product centered
- **Flat lay:** top-down arrangement on a textured surface, even light
- **Lifestyle:** product in use in a real setting, natural light, candid feel
- **Editorial fashion:** bold poses, dramatic light, styled set
- **Food:** 45-degree or overhead angle, appetizing side light, visible texture and steam
- **UI / app mockup:** device on a desk, screen content described precisely
- **Packaging and label design:** dieline flat view or 3D rendered box, printed text quoted exactly
- **Logo / wordmark:** flat vector, limited palette, simple geometric construction, plain background
- **Icon set / stickers:** consistent stroke weight, shared palette, white background

## Color and mood

- Palettes: monochrome, duotone, complementary teal-and-orange, earthy terracotta and sage, pastel, neon on black, muted desaturated, high-key white, low-key dark
- Name brand colors by hex or description when consistency matters ("deep navy #1B2A4A")
- Mood words that change output: serene, contemplative, whimsical, playful, ominous, triumphant, nostalgic, clinical, cozy, luxurious

## Aspect ratios and pixel sizes

Output dimensions per `imageSize` (from Google's image generation docs). `nb2` and `pro` produce the same dimensions for the shared ratios; `pro` has no `512` size and no extreme ratios; `nb2-lite` is 1K only (same 1K dimensions as `nb2`).

| Ratio | 512 (nb2) | 1K | 2K | 4K | Typical use |
|-------|-----------|----|----|----|-------------|
| 1:1 | 512x512 | 1024x1024 | 2048x2048 | 4096x4096 | Social post, avatar, icon, sticker |
| 4:5 | 464x576 | 928x1152 | 1856x2304 | 3712x4608 | Instagram portrait, feed ad |
| 5:4 | 576x464 | 1152x928 | 2304x1856 | 4608x3712 | Print photo |
| 3:4 | 448x600 | 896x1200 | 1792x2400 | 3584x4800 | Vertical art, book cover |
| 4:3 | 600x448 | 1200x896 | 2400x1792 | 4800x3584 | Slides, classic photo |
| 2:3 | 424x632 | 848x1264 | 1696x2528 | 3392x5056 | Poster |
| 3:2 | 632x424 | 1264x848 | 2528x1696 | 5056x3392 | DSLR-style photo |
| 9:16 | 384x688 | 768x1376 | 1536x2752 | 3072x5504 | Stories, Reels, phone wallpaper, vertical video keyframe |
| 16:9 | 688x384 | 1376x768 | 2752x1536 | 5504x3072 | Thumbnail, desktop, widescreen video keyframe |
| 21:9 | see note | 1584x672 | 3168x1344 | 6336x2688 | Cinematic banner, ultrawide |
| 1:4 (nb2) | 256x1024 | 512x2048 | 1024x4096 | 2048x8192 | Vertical web banner |
| 4:1 (nb2) | 1024x256 | 2048x512 | 4096x1024 | 8192x2048 | Horizontal web banner |
| 1:8 (nb2) | 192x1536 | 384x3072 | 768x6144 | 1536x12288 | Skyscraper banner |
| 8:1 (nb2) | 1536x192 | 3072x384 | 6144x768 | 12288x1536 | Leaderboard strip |

Note: Google's table lists 21:9 at 512 as 792x168, which does not match the ratio; check the returned `width`/`height` if you use it.

Veo video only accepts 16:9 and 9:16, so generate video keyframes at one of those two ratios.
