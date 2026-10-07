# Structure, genres and lyrics

Vocabulary and templates for `gemini-media:generate_music`.

## Contents

- [Prompt anatomy](#prompt-anatomy)
- [Structure tags](#structure-tags)
- [Timestamp layouts](#timestamp-layouts)
- [Templates by use](#templates-by-use)
- [Tempo](#tempo)
- [Key and mode](#key-and-mode)
- [Genres and their ingredients](#genres-and-their-ingredients)
- [Production descriptors](#production-descriptors)
- [Vocals](#vocals)
- [Writing lyrics](#writing-lyrics)
- [Images as inspiration](#images-as-inspiration)

## Prompt anatomy

```text
[Length and purpose] [genre] [mood/energy] in [key] at [BPM], with [lead instrument],
[rhythm section], [texture/pads]. [Vocal description or "Instrumental only"].
[Production style]. [Structure tags or timestamps].
```

Examples that work (from Google's Lyria guide):

- "A 30-second lofi hip hop beat with dusty vinyl crackle, mellow Rhodes piano chords, a slow boom-bap drum pattern at 85 BPM, and a jazzy upright bass line. Instrumental only."
- "An upbeat, feel-good pop song in G major at 120 BPM with bright acoustic guitar strumming, claps, and warm vocal harmonies about a summer road trip."
- "A dark, atmospheric trap beat at 140 BPM with heavy 808 bass, eerie synth pads, sharp hi-hats, and a haunting vocal sample. In D minor."
- "A bright chiptune melody in C Major, retro 8-bit video game style. Instrumental only, no vocals."

## Structure tags

Documented tags: `[Intro]`, `[Verse]` (numbered: `[Verse 1]`, `[Verse 2]`), `[Chorus]`, `[Bridge]`, `[Outro]`. Tags such as `[Pre-Chorus]`, `[Hook]`, `[Build]`, `[Drop]` or `[Instrumental Break]` are not documented but are common songwriting labels worth trying.

Under each tag, describe the arrangement or put the lyrics:

```text
[Intro] Ambient synth pad, ethereal and spacious
[Verse 1] Lo-fi beat enters, mellow piano chords, laid-back vocal
[Chorus] Lift: strings and fuller drums, brighter melody
[Bridge] Strip back to piano and a single soft vocal
[Outro] Fade out on reverb tails
```

Lyria reasons about song structure before generating audio, so a clear section plan improves coherence. `clip` output is only 30 s: use at most two or three short sections there.

## Timestamp layouts

Timestamps pin sections to times: `[start - end] Section: description`.

30-second clip (jingle or bed):

```text
[0:00 - 0:05] Intro: single plucked guitar motif
[0:05 - 0:25] Main: full groove, bass and brushed drums, motif repeats
[0:25 - 0:30] Ending: resolve on the tonic, ring out
```

Two-and-a-half-minute song (`full`):

```text
[0:00 - 0:12] Intro
[0:12 - 0:40] Verse 1
[0:40 - 1:05] Chorus
[1:05 - 1:30] Verse 2
[1:30 - 1:55] Chorus
[1:55 - 2:15] Bridge
[2:15 - 2:30] Final chorus and outro
```

Pair the section list with the style description above it, and keep lyrics in `lyrics` with matching section tags.

## Templates by use

| Use | Model | Prompt skeleton |
|-----|-------|-----------------|
| Podcast intro sting | `clip` | "A confident 30-second [genre] intro with a memorable [instrument] hook in the first 5 seconds, building to a clean ending. Instrumental." |
| Bed under voiceover | `clip` or `full` + `instrumental` | "Sparse, steady [genre] bed at [BPM], no prominent lead melody, leaves room for voiceover, consistent energy throughout." |
| Social ad (15-30 s) | `clip` | "Punchy [genre], immediate energy from the first beat, [hook instrument], ends on a hit at 0:30." |
| Game loop | `clip` | "Seamless, loopable [genre] at [BPM], steady energy, no intro or ending." Trim the loop point with ffmpeg afterwards. |
| Film cue | `full` | Timestamps that follow the scene: "[0:00 - 0:20] tense low strings... [0:20 - 0:45] brass swells as..." |
| Full song | `full` + `lyrics` | Genre, mood, tempo, key, vocal description, then tagged lyrics. |

## Tempo

| Feel | BPM |
|------|-----|
| Ballad, ambient, slow | 60-80 |
| Moderate, lo-fi, hip hop | 80-100 |
| Pop, rock, funk | 100-130 |
| House, dance | 120-130 |
| Techno, trance | 130-145 |
| Drum and bass | 160-180 |

Also useful: half-time feel, double-time hi-hats, swing, straight eighths, rubato.

## Key and mode

- Major keys read bright and open (C, G, D, E major); minor keys read darker or moodier (A, E, D minor).
- Modes for color: Dorian (jazzy minor, funk), Mixolydian (bluesy rock), Lydian (dreamy, cinematic), Phrygian (tense, flamenco, metal).
- A key is a hint, not a guarantee; state mood explicitly as well.

## Genres and their ingredients

| Genre | Typical ingredients |
|-------|---------------------|
| Lo-fi hip hop | Detuned Rhodes or piano, vinyl crackle, dusty boom-bap drums, tape wobble |
| Pop | Bright synths or acoustic guitar, claps, catchy chorus hook, polished vocals |
| Indie folk | Fingerpicked acoustic guitar, banjo or mandolin, soft harmonies, room sound |
| Rock | Electric guitars with power chords, bass guitar, live drum kit |
| Jazz | Upright bass, brushed drums, saxophone or trumpet, Rhodes or piano comping |
| Electronic / house | Four-on-the-floor kick, off-beat hats, synth stabs, side-chained pads |
| Synthwave | Analog arpeggios, gated reverb drums, neon 1980s pads |
| Trap | 808 bass, rolling hi-hats, sparse dark melody |
| Cinematic / orchestral | Strings, brass swells, timpani, choir, dynamic build |
| Ambient | Evolving pads, field recordings, slow swells, no strong beat |
| Classical | String quartet, solo piano, woodwinds, chamber intimacy |
| Afrobeats | Syncopated percussion, log drums or shakers, bright guitar licks |
| Bossa nova | Nylon guitar, soft brushed percussion, gentle vocals |
| Chiptune | Square-wave leads, 8-bit arpeggios, noise-channel drums |

## Production descriptors

- Warm and nostalgic: lo-fi, vinyl crackle, tape saturation, room reverb
- Modern and clean: crystal clear, polished, radio-ready, wide stereo
- Raw: garage, live-off-the-floor, DIY, analog
- Spacious: reverb-heavy, ethereal, cavernous, shimmer
- Tight: punchy, compressed, dry, upfront drums

## Vocals

- Voice type and texture: breathy, raspy, gravelly, clear, falsetto, belting, spoken-word, rap flow
- Arrangement: lead with harmonies, call and response, choir, gang vocals on the chorus, vocal chops
- Language: lyrics are sung in the language they are written in; the model adapts pronunciation and vocal style to it
- No vocals: `instrumental: true`

## Writing lyrics

- Tag every section (`[Verse 1]`, `[Chorus]`, ...) and repeat the chorus text where it should recur.
- Keep lines short and similar in syllable count within a section; simple end rhymes help melody.
- Give the chorus the title phrase and the simplest words.
- Write what is sung, nothing else: no stage directions inside the lyric lines.
- Original words only. Do not paste existing song lyrics.
- Match the lyric language to the target audience and write the prompt in the same language for best results.

## Images as inspiration

Pass up to 10 images in `images` (paths or `gemini-media://` URIs, for example a storyboard frame or cover art). Add a sentence connecting them to the music ("score the mood of this rainy neon street"); the images guide mood and scene, not exact instrumentation.
