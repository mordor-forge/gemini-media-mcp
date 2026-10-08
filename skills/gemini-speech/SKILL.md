---
name: gemini-speech
description: Generates natural speech audio (WAV) with Gemini text-to-speech through the gemini-media MCP server - voiceovers, narration, audiobook passages, announcements, explainer and ad reads, and two-speaker podcast or dialogue scripts, with controllable delivery style, 30 prebuilt voices, inline sounds like sighs and pauses, and automatic language detection. Use when the user wants text read aloud, a voiceover or narration track, TTS, a spoken intro, or a conversation rendered as audio. Not for music or singing (use gemini-music), transcription or speech-to-text, dialogue inside a generated video clip (gemini-video), or live voice chat.
license: Apache-2.0
compatibility: Requires the gemini-media MCP server (https://github.com/mordor-forge/gemini-media-mcp) with a Gemini API key or Vertex AI project.
metadata:
  author: mordor-forge
  version: "1.1.0"
  mcp-server: gemini-media
---

# Gemini Speech (TTS)

## Quick start

1. Get the exact words to speak. If you are writing them, write for the ear (see below).
2. Choose a voice (default `Aoede`) and a short delivery `style`, e.g. `"warm, unhurried documentary narrator"`.
3. Call `gemini-media:generate_speech` with `text`, `voice` and `style`.
4. The result is a WAV file (`file.path`, `file.uri`) with `durationSeconds`. Check the duration matches the script (roughly 2.5 words per second at a normal pace).
5. Adjust `style`, `voice` or the wording and regenerate as needed.

## Tools

The tools live on the `gemini-media` MCP server. Harnesses name MCP tools differently (`mcp__gemini-media__generate_speech`, `gemini-media.generate_speech`, or plain `generate_speech`); match on the part after the server name. This skill writes them as `gemini-media:<tool>`.

| Tool | Use it to |
|------|-----------|
| `gemini-media:generate_speech` | Synthesize one voice (`text`) or a two-speaker conversation (`dialogue`) |
| `gemini-media:list_models` | Current speech models and prices; `detail: true` lists all voices |
| `gemini-media:estimate_cost` | Price a long script (`mediaType: "speech"`, `text`) |
| `gemini-media:get_usage` | Spend so far and budget remaining |

## The one rule: text is spoken verbatim

The default model (`tts`, Gemini 3.8 Flash TTS) and `tts-lite` read `text` exactly as written. Anything you put there is spoken.

- **Sustained delivery** (tone, pace, emotion, accent, character) goes in `style`, or per line in `dialogue[].style`. Never write "Say cheerfully:", "(whispering)", "Narrator:" or stage directions in `text`.
- **Point-in-time sounds** go inline in `text` as angle-bracket tags at the exact spot: `<sigh>`, `<laugh>`, `<chuckle>`, `<breath>`, `<gasp>`, `<cough>`, `<short pause>`, `<long pause>`. Full list in [references/voices.md](references/voices.md).
- **Pacing:** punctuation (`...`, `--`, commas), pause tags, or a `style` like `"speaking slowly"`.
- **Emphasis:** capitalize the stressed word: "This is a VERY important point."
- Always use `style` even with legacy models (`tts-2.5`, `tts-3.1`, `tts-pro`): the server converts it into the directions those models expect.

## Parameters that matter

| Parameter | Guidance |
|-----------|----------|
| `text` | Single-speaker script. Exact words plus inline tags only. |
| `dialogue` | Instead of `text`: `[{speaker, text, style?}]`, one entry per line, at most 2 distinct speakers. |
| `voice` | Single-speaker voice name (30 prebuilt, case-insensitive). Default: the server's configured voice, normally `Aoede`. |
| `speakers` | Dialogue voices: `[{speaker: "Host", voice: "Charon"}, {speaker: "Guest", voice: "Puck"}]`. Unlisted speakers get distinct defaults. |
| `style` | Delivery for the whole request; a line's own `style` overrides it. Keep it to a short phrase. |
| `languageCode` | Optional BCP-47 code (`it-IT`, `pt-BR`). The language is auto-detected; set it for very short or mixed-language text. |
| `model` | `tts` (default) or `tts-lite` (cheaper, fewer languages, good for bulk). |
| `format` | `wav` (default, 24 kHz mono 16-bit) or `pcm` (raw, headerless) for custom pipelines. |
| `outputName` | Readable file name, e.g. `episode-12-intro`. |

## Choosing a voice

Each prebuilt voice has a character. Good starting points:

| Need | Voices |
|------|--------|
| Neutral narration, explainers | Charon (informative), Iapetus (clear), Schedar (even), Sadaltager (knowledgeable) |
| Warm, friendly reads | Sulafat (warm), Achird (friendly), Vindemiatrix (gentle), Aoede (breezy) |
| Energetic ads, hype | Puck (upbeat), Laomedeia (upbeat), Fenrir (excitable), Sadachbia (lively) |
| Authority, announcements | Kore (firm), Orus (firm), Alnilam (firm) |
| Calm, intimate, meditation | Achernar (soft), Enceladus (breathy) |
| Characters | Algenib (gravelly), Gacrux (mature), Leda (youthful), Zubenelgenubi (casual) |

All 30 with descriptions: [references/voices.md](references/voices.md). For a two-person show pick contrasting voices (e.g. Charon + Puck, Kore + Achird) so listeners can tell them apart. When the choice matters, generate the same short line with two or three voices and let the user pick.

## Workflow

1. **Script.** Confirm or write the exact words. Decide single voice or dialogue.
2. **Write for the ear.** Short sentences; one idea each; spell out numbers, dates, units and symbols as they should be said ("four ninety-nine", "twenty twenty-seven"); expand abbreviations; spell tricky names phonetically; remove markdown, bullets, URLs (or say them: "example dot com").
3. **Voice and style.** Pick from the table; write a short `style`. For dialogue, give each line its own `style` only where the emotion changes.
4. **Generate** with `gemini-media:generate_speech`.
5. **Check** `durationSeconds`, `voices` and `warnings` in the result. Most agents cannot listen: say so and let the user judge the performance.
6. **Iterate** one variable at a time: `style` wording first, then voice, then the script.
7. **Long scripts:** one call returns up to about 10 minutes. Split longer material at paragraph or scene breaks, keep voice and `style` identical across parts, and join the WAVs (see **gemini-media-production**).

## Examples

```text
generate_speech(voice: "Charon", style: "calm, authoritative documentary narrator, measured pace",
text: "In the high Andes, water is a matter of timing. <short pause> Every morning, the glacier gives a little back.")

generate_speech(voice: "Puck", style: "excited, fast-paced radio ad",
text: "This weekend only... EVERYTHING in the store is half price! <laugh> Yes, really.")

generate_speech(
  speakers: [{speaker: "Host", voice: "Charon"}, {speaker: "Guest", voice: "Laomedeia"}],
  style: "relaxed, conversational podcast",
  dialogue: [
    {speaker: "Host", text: "So the launch is Thursday |oh hmm| Are we actually ready?"},
    {speaker: "Guest", text: "Ready enough. <chuckle> The last blocker cleared this morning.", style: "confident, amused"},
    {speaker: "Host", text: "Then let's ship it."}])
```

In dialogue, `|reaction|` inside a line adds the other speaker's short backchannel ("|oh hmm|", "|absolutely|") without a separate turn (documented for the default `tts` model).

## Languages

Language is detected from the text: Gemini 3.8 Flash TTS covers more than 130 languages, Flash-Lite more than 100. Write the text in the target language and keep inline tags exactly as listed (English tag names). To produce several language versions, translate the script first, then generate each with the same voice and `style`.

## Cost and approvals

- Speech is cheap (cents per minute of audio), but long scripts add up. For more than a few minutes of audio or batches, call `gemini-media:estimate_cost` with `mediaType: "speech"` and the `text`, and use `tts-lite` for bulk drafts.
- `[confirmation]` error: show the user the amount; only after they agree, retry with `approvedCostUsd`. Never invent approval.
- `[budget]` error: a cap was reached. Stop and show `gemini-media:get_usage`.

## Errors

Tool errors read `[kind] message` then `Hint: ...`. Follow the hint; in short:

| Kind | Do |
|------|----|
| `invalid` | Usually: both or neither of `text`/`dialogue`, more than 2 speakers, an unknown voice name (the message lists valid ones), or a bad `format`. Fix and retry. |
| `safety` | Rephrase or remove the flagged content; do not resend unchanged. |
| `quota`, `unavailable` | The server already retried with backoff. Wait briefly, retry once, then report. |
| `timeout` | Not retried by the server (the request may still complete and bill). Retry once at most; for long scripts, split the text into shorter calls. |
| `not_found` | Model not available on this backend: check `gemini-media:list_models`. |
| `auth`, `permission` | Stop. Ask the user to run `gemini-media-mcp doctor`. |
| `budget`, `confirmation` | See Cost and approvals. |

Gotchas:

- Gemini 3.8 TTS runs on the Gemini API. On Vertex AI the server falls back to a legacy model and says so in `warnings`; `style` still works, but angle-bracket tags may be read aloud or ignored, so drop them there.
- If the user hears directions spoken aloud ("say warmly..."), they were in `text`: move them to `style` and regenerate.

## Interaction mode

- **With a user present:** confirm the script wording and the voice when either is unclear; offer two or three voice samples for important reads. After delivery, suggest concrete next steps (different style, second language, music bed).
- **Autonomous (nobody to ask):** do not block. Choose a voice from the table that fits the purpose, write a short `style`, generate once, and fix only clear problems (wrong language, directions read aloud). Report script, voice, style, file path, duration and cost.

## Chaining

- Music bed or intro sting: **gemini-music**. Visuals: **gemini-image**, **gemini-video** (Veo can also speak short lines inside a clip).
- Mixing voiceover over music, ducking, joining parts, and syncing to video: **gemini-media-production**.

## References

- [references/voices.md](references/voices.md): all 30 voices, style vocabulary, the full inline tag list, dialogue and pacing techniques.
