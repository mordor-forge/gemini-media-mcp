# Voices, style and tags

Reference for `gemini-media:generate_speech` with the default Gemini 3.8 TTS models (`tts`, `tts-lite`).

## Contents

- [Prebuilt voices](#prebuilt-voices)
- [Defaults and custom voices](#defaults-and-custom-voices)
- [Style vocabulary](#style-vocabulary)
- [Inline tags](#inline-tags)
- [Pacing, emphasis and prosody](#pacing-emphasis-and-prosody)
- [Dialogue techniques](#dialogue-techniques)
- [Writing for the ear](#writing-for-the-ear)
- [Legacy models](#legacy-models)

## Prebuilt voices

The one-word character comes from Google's voice list; the "good for" column is a starting suggestion. Audition two or three candidates when the voice matters.

| Voice | Character | Good for |
|-------|-----------|----------|
| Zephyr | Bright | Upbeat explainers, product intros |
| Puck | Upbeat | Ads, energetic hosts, second podcast voice |
| Charon | Informative | Documentary narration, news, e-learning |
| Kore | Firm | Announcements, confident brand reads |
| Fenrir | Excitable | Hype, sports, gaming |
| Leda | Youthful | Young characters, casual social content |
| Orus | Firm | Corporate, instructions, authority |
| Aoede | Breezy | General purpose (server default) |
| Callirrhoe | Easy-going | Relaxed conversation, lifestyle |
| Autonoe | Bright | Cheerful tutorials, kids' content |
| Enceladus | Breathy | Intimate, ASMR-like, late-night reads |
| Iapetus | Clear | Technical explainers, accessibility reads |
| Umbriel | Easy-going | Laid-back podcast host |
| Algieba | Smooth | Premium brand voiceover, audiobooks |
| Despina | Smooth | Audiobooks, calm product stories |
| Erinome | Clear | Instructions, IVR prompts, tutorials |
| Algenib | Gravelly | Trailers, gritty characters |
| Rasalgethi | Informative | Lectures, documentary |
| Laomedeia | Upbeat | Lively co-host, promos |
| Achernar | Soft | Meditation, bedtime stories |
| Alnilam | Firm | Serious announcements, finance |
| Schedar | Even | Long-form narration, neutral reads |
| Gacrux | Mature | Elder characters, gravitas |
| Pulcherrima | Forward | Direct-response ads, pitches |
| Achird | Friendly | Customer-facing messages, onboarding |
| Zubenelgenubi | Casual | Vlogs, informal conversation |
| Vindemiatrix | Gentle | Wellness, children's stories |
| Sadachbia | Lively | Entertainment, event promos |
| Sadaltager | Knowledgeable | Expert explainer, interviews |
| Sulafat | Warm | Welcome messages, heartfelt narration |

`gemini-media:list_models` with `mediaType: "speech"` and `detail: true` returns the current voice list.

## Defaults and custom voices

- Single speaker: `voice`, else the server's configured default, else `Aoede`.
- Dialogue: the first speaker gets that default; the second gets `Puck` (or `Kore` if the first is `Puck`), unless `speakers` assigns voices.
- Voice names are case-insensitive; an unknown name returns an `invalid` error that lists the valid ones.
- Single-speaker requests on the 3.8 models also accept a voice ID from Google's Extended Voice Library, Voice design or Voice replication (IDs such as `voice_...` or `voicekey_...`). Dialogue uses prebuilt voices.

## Style vocabulary

`style` is a short phrase describing sustained delivery for the whole request or one dialogue line. Combine a few of these dimensions:

| Dimension | Examples |
|-----------|----------|
| Emotion | warm, enthusiastic, sarcastic, anxious, angry tone, tender, amused, solemn |
| Energy | calm, relaxed, excited, high energy, tired, out of breath |
| Pace | speaking slowly, speaking rapidly, measured, unhurried, brisk |
| Volume and texture | whispers, whispered urgently, muttering, projecting to a crowd, soft |
| Pitch and inflection | high pitch, cheerful and excited inflection; monotone and flat; low and resonant |
| Persona | documentary narrator, late-night radio host, sports commentator, friendly teacher |
| Accent | a specific regional accent, e.g. "light Scottish accent", "Southern US drawl" |
| Arc within a line | "muttering, then reassuring" |

Good: `"slow, calm narrator"`, `"warm and enthusiastic"`, `"casual, friendly"`. Avoid long director's notes or character biographies in `style`; keep it to what the listener should hear.

## Inline tags

Place tags in `text` exactly where the sound should happen: `"Wait... <short pause> did you hear that? <sigh>"`.

| | | | |
|---|---|---|---|
| `<argh>` | `<breath>` | `<heavy breath>` | `<exhales>` |
| `<cackle>` | `<cheer>` | `<chuckle>` / `<chuckles>` | `<cough>` |
| `<cry>` | `<gasp>` | `<giggle>` | `<groan>` |
| `<growl>` | `<grunt>` | `<grr>` | `<hiss>` |
| `<laugh>` / `<laughter>` | `<moan>` | `<pant>` | `<pff>` / `<phew>` |
| `<scream>` | `<shout>` | `<shriek>` | `<sigh>` / `<sighs>` |
| `<sneeze>` | `<snicker>` | `<snort>` | `<sob>` |
| `<throat-clearing>` | `<tsk>` | `<whimper>` | `<whispers>` / `<whispering>` |
| `<yawn>` | `<short pause>` | `<long pause>` | |

Use tags sparingly; one or two per sentence at most.

## Pacing, emphasis and prosody

- **Pauses:** commas and full stops; `...` for hesitation; `--` for an abrupt break; `<short pause>` and `<long pause>` for explicit silence.
- **Overall speed:** `style: "speaking slowly"` or `"speaking rapidly"`.
- **Emphasis:** capitalize the stressed word: `"It was a VERY long day <sigh> ... nobody listens anymore."`
- **Prosody shifts:** when emotion or pitch changes mid-script, split it into separate lines (dialogue entries or separate calls) with their own `style`.
- **Natural conversation:** since text is verbatim, write the disfluencies you want to hear: `"Oh, uh, yeah, I think... hm, so that's interesting."`

## Dialogue techniques

- At most two distinct speakers per call. Use stable speaker names (`Host`, `Guest`) and map them with `speakers`.
- One entry per turn. Give a line its own `style` only where delivery changes; the top-level `style` covers the rest.
- Backchannels: wrap the listener's short reaction in pipes inside the current speaker's line: `"Ready enough |oh really?| The last blocker cleared this morning."` (documented for the default `tts` model).
- Keep turns short and alternating; long monologues sound more natural as single-speaker calls.
- Three or more characters: generate separate calls (each with one or two voices) and join the files (see **gemini-media-production**).
- For consistent voices across a series, reuse the same voice names, `style` wording and model in every call.

## Writing for the ear

- Short sentences with one idea each; put the key word near the end.
- Spell out numbers, currencies, dates, times and units the way they should be said: "$4.99" as "four ninety-nine", "2027" as "twenty twenty-seven", "5 km" as "five kilometers".
- Expand abbreviations and acronyms unless they are said as letters ("N-A-S-A" vs "NASA").
- Respell hard names phonetically if the first take mispronounces them ("Nguyen" as "Nwin").
- Remove markdown, bullets, emoji, parentheses and URLs, or rewrite them as speech ("example dot com slash pricing").
- Read the script against the target length: about 150 words per minute at a normal pace; slower styles run longer.

## Legacy models

`tts-3.1`, `tts-2.5` and `tts-pro` are older models that take delivery directions as natural language inside the prompt (and use bracketed cues such as `[whispers]` rather than angle-bracket tags). The server builds those directions from `style` and per-line `style` automatically, so keep using `style`. The server also uses a legacy model as a fallback on Vertex AI, where the 3.8 models are not offered; results then carry a warning.
