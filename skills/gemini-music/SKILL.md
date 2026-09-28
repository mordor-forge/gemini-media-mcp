---
name: gemini-music
description: Composes original music with Google Lyria through the gemini-media MCP server - 30-second clips, loops, jingles and stingers, or full songs of a few minutes with vocals, custom lyrics in any language, section structure and timing control, instrumental beds for videos and podcasts, and music inspired by images. Use when the user wants a song, beat, soundtrack, theme tune, background or intro music, or describes a musical idea to hear, even without naming Lyria. Not for spoken voiceover or narration (use gemini-speech), sound effects inside a video clip, or editing, remixing or extending an existing audio file.
license: Apache-2.0
compatibility: Requires the gemini-media MCP server (https://github.com/mordor-forge/gemini-media-mcp) with a Gemini API key or Vertex AI project.
metadata:
  author: mordor-forge
  version: "1.0.0"
  mcp-server: gemini-media
---

# Gemini Music (Lyria)

## Quick start

1. Describe the music: genre, mood, instrumentation, tempo, key, production style, and what it is for.
2. Call `gemini-media:generate_music` with `prompt` (default model `clip`: a 30-second MP3). Add `instrumental: true` for beds under speech or video.
3. The result has `file.path` / `file.uri` and usually `lyrics`: the lyrics or section structure the model produced. Use that text to check what you got.
4. Iterate by changing the prompt and generating again, then move to `full` for a complete song.

## Tools

The tools live on the `gemini-media` MCP server. Harnesses name MCP tools differently (`mcp__gemini-media__generate_music`, `gemini-media.generate_music`, or plain `generate_music`); match on the part after the server name. This skill writes them as `gemini-media:<tool>`.

| Tool | Use it to |
|------|-----------|
| `gemini-media:generate_music` | Compose a clip or a full song from text, optional lyrics and up to 10 inspiration images |
| `gemini-media:estimate_cost` | Price a batch (`mediaType: "music"`, `model`, `count`) |
| `gemini-media:list_models` | Current music models, prices and formats |
| `gemini-media:get_usage` | Spend so far and budget remaining |

## Models at a glance

| Alias | Choose it for | Output |
|-------|---------------|--------|
| `clip` (default) | Exploring ideas, jingles, stingers, loops, short social beds | Always 30 s, MP3 |
| `full` | Complete songs with verses, choruses, vocals and lyrics; long beds | Up to about 3 min, MP3 or WAV (44.1 kHz stereo) |

`full` maps to Lyria 3.5 on the Gemini API; on Vertex AI the server falls back to the previous full-song model (MP3 only) and says so in `warnings`. Prices change: check `gemini-media:list_models` (`mediaType: "music"`) or `gemini-media:estimate_cost` rather than quoting from memory.

## Parameters that matter

| Parameter | Guidance |
|-----------|----------|
| `prompt` | The musical brief, plus structure tags or timestamps (below). |
| `lyrics` | Your own lyrics with section tags. They are sung in the language they are written in. |
| `instrumental` | `true` for no vocals (appends "Instrumental only, no vocals" to the prompt). |
| `bpm` | Tempo hint, added to the prompt. |
| `durationSeconds` | Length hint for `full` only; `clip` is always 30 s and ignores it (with a warning). |
| `images` | Up to 10 images whose mood or scene should inspire the music (paths or `gemini-media://` URIs). |
| `format` | `mp3` (default) or `wav` (`full` on the Gemini API only). |
| `outputName` | Readable file name, e.g. `podcast-intro-v3`. |

`bpm`, `instrumental` and `durationSeconds` are folded into the prompt as text; Lyria has no hard controls for tempo, vocals or length, so treat them as hints and check the result.

## Workflow

1. **Intent.** Purpose (background bed, intro sting, standalone song), genre and references described in musical terms, mood and energy, vocals or instrumental, length.
2. **Prompt.** Genre + mood + instruments + tempo + key + production, then structure. Templates and vocabulary: [references/structure-and-genres.md](references/structure-and-genres.md).
3. **Model.** `clip` for exploration and anything that fits in 30 s. `full` for songs, lyrics you care about, or beds longer than 30 s.
4. **Generate** with `gemini-media:generate_music`.
5. **Review.** Most agents cannot listen. Check the returned `lyrics`/structure text, `warnings` and file duration, and let the user judge the sound.
6. **Iterate.** Every generation is independent: refine the prompt (one or two changes at a time) and generate again. You cannot edit or extend a generated track.
7. **Deliver** path, model, cost and the returned lyrics. For exact lengths, loops or fades under video, trim with ffmpeg (see **gemini-media-production**).

## Prompt craft

- **Be specific about the music, not the feeling alone.** "A 30-second lo-fi hip hop beat with dusty vinyl crackle, mellow Rhodes chords, a slow boom-bap pattern at 85 BPM and a jazzy upright bass" beats "chill music".
- **Structure tags** shape full songs: `[Intro]`, `[Verse]` (or `[Verse 1]`), `[Chorus]`, `[Bridge]`, `[Outro]`. Describe the arrangement under each tag.
- **Timestamps** control when things happen: `[0:00 - 0:10] Intro: soft piano and vinyl crackle`.
- **Length** for `full`: say it in the prompt ("a 2-minute song"), set `durationSeconds`, or lay out timestamps to the end time.
- **Lyrics:** write them in `lyrics` with section tags; keep lines singable (even syllable counts, simple rhymes). For non-English vocals, write the lyrics (and ideally the prompt) in that language.
- **Vocal style:** describe it ("breathy female lead with warm harmonies", "gravelly male baritone", "gospel choir on the chorus").
- **Beds under speech:** instrumental, steady energy, no prominent lead melody in the speech range; say "sparse, leaves room for voiceover".
- **Describe styles, not artists.** Requests to imitate a named artist or reproduce existing lyrics may be refused; describe era, instruments, production and vocal character instead.

Examples:

```text
generate_music(instrumental: true, bpm: 92, prompt:
"Warm acoustic folk bed for a travel vlog: fingerpicked guitar, light shaker, soft upright bass,
gentle glockenspiel accents. Optimistic and unhurried, sparse enough to sit under voiceover.")

generate_music(model: "full", durationSeconds: 150, prompt:
"An upbeat, feel-good pop song in G major at 120 BPM with bright acoustic guitar strumming,
claps and warm vocal harmonies about a summer road trip.",
lyrics: "[Verse 1]\nWindows down on the coastal road...\n[Chorus]\nWe're chasing the sun...")

generate_music(model: "full", format: "wav", prompt:
"[0:00 - 0:10] Intro: soft lo-fi beat and muffled vinyl crackle.
[0:10 - 0:30] Verse: warm Rhodes melody, gentle vocals about a rainy morning.
[0:30 - 0:50] Chorus: full band, upbeat drums, soaring synth leads, hopeful lyrics.
[0:50 - 1:00] Outro: fade out with the Rhodes alone.")
```

## Cost and approvals

- Music is priced per generation (`full` costs more than `clip`). Explore with `clip`; switch to `full` once the direction is agreed.
- For batches (several songs or many variations) call `gemini-media:estimate_cost` with `mediaType: "music"`, `model` and `count`, and tell the user the total first.
- `[confirmation]` error: show the user the amount; only after they agree, retry with `approvedCostUsd`. Never invent approval.
- `[budget]` error: a cap was reached. Stop and show `gemini-media:get_usage`.

## Errors

Tool errors read `[kind] message` then `Hint: ...`. Follow the hint; in short:

| Kind | Do |
|------|----|
| `invalid` | Usually `format: "wav"` with `clip`, more than 10 images, or a non-image input. Fix and retry. |
| `safety` | Often an artist name or copyrighted lyrics: describe the style instead and rewrite the lyrics. Do not resend unchanged. |
| `quota`, `unavailable` | The server already retried with backoff. Wait briefly, retry once, then report. |
| `timeout` | Not retried by the server (the generation may still complete and bill). Retry once at most. |
| `not_found` | Model not offered on this backend: check `gemini-media:list_models`. |
| `auth`, `permission` | Stop. Ask the user to run `gemini-media-mcp doctor`. |
| `budget`, `confirmation` | See Cost and approvals. |

## Interaction mode

- **With a user present:** if genre, vocals or length is unclear, ask one short question or propose a direction and generate a `clip` to react to. After delivery, suggest concrete next steps (a `full` version, lyrics, a different tempo or instrumentation).
- **Autonomous (nobody to ask):** do not block. Infer genre and energy from the purpose, default to `clip` (or `full` + `instrumental` when a bed must exceed 30 s), stay below the confirmation threshold, and generate once or twice at most. Report the prompt, model, file path, returned structure and cost.

## Chaining

- Voiceover over the music: **gemini-speech**. Cover art or visuals: **gemini-image**; the art can also feed `images` to inspire the track.
- Music under a video, podcast intros with ducking, trimming and looping: **gemini-media-production**.

## References

- [references/structure-and-genres.md](references/structure-and-genres.md): structure tags, timestamp layouts, genre, instrument, tempo and key vocabulary, and lyric-writing tips.
