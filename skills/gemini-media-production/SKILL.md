---
name: gemini-media-production
description: Plans and produces multi-asset media projects with the gemini-media MCP server by chaining Nano Banana images, Veo or Gemini Omni video, Gemini TTS voiceover and Lyria music into one deliverable - product promos, social ads, short story or explainer videos, trailers, podcast intros. Covers shot lists, consistent characters across shots, an up-front budget with cost estimates and approval, parallel video jobs, and assembly with ffmpeg. Use when a request needs two or more media types or several shots combined, even if the user just says "make a promo video". For a single image, clip, voiceover or track, use gemini-image, gemini-video, gemini-speech or gemini-music instead.
license: Apache-2.0
compatibility: Requires the gemini-media MCP server (https://github.com/mordor-forge/gemini-media-mcp) with a Gemini API key or Vertex AI project. Optional - a shell with ffmpeg for assembly.
metadata:
  author: mordor-forge
  version: "1.0.0"
  mcp-server: gemini-media
---

# Gemini Media Production

Orchestrates the four generation skills into one finished piece. Each asset type has its own skill with the detailed prompt craft; load it when you reach that stage: **gemini-image** (keyframes, characters, cards), **gemini-video** (Veo and Omni shots, edits and job handling), **gemini-speech** (voiceover and dialogue), **gemini-music** (score and beds).

## Quick start

1. **Brief:** goal, audience, platform (ratio and length), tone, must-haves (product, logo, lines of copy), deadline for approval.
2. **Plan:** write the shot list, voiceover script and music cue (template below).
3. **Budget:** price every asset with `gemini-media:estimate_cost`, total it, get approval.
4. **Lock the look:** generate keyframes and a character or product sheet with `gemini-media:generate_image`; iterate cheaply until they are right.
5. **Animate:** start one `gemini-media:generate_video` job per shot from the approved keyframes, then collect them with `gemini-media:get_video`.
6. **Voice:** `gemini-media:generate_speech` for the script, timed to the shots.
7. **Music:** `gemini-media:generate_music`, usually instrumental, sized to the runtime.
8. **Assemble** with ffmpeg if the environment has a shell ([references/ffmpeg-recipes.md](references/ffmpeg-recipes.md)); otherwise deliver the assets with an edit list.
9. **Report:** final file(s), every asset path, total spend from `gemini-media:get_usage`.

## Tools

The tools live on the `gemini-media` MCP server. Harnesses name MCP tools differently (`mcp__gemini-media__generate_video`, `gemini-media.generate_video`, or plain `generate_video`); match on the part after the server name. This skill writes them as `gemini-media:<tool>`.

Generation: `gemini-media:generate_image`, `gemini-media:edit_image`, `gemini-media:generate_video`, `gemini-media:get_video`, `gemini-media:extend_video`, `gemini-media:edit_video`, `gemini-media:generate_speech`, `gemini-media:generate_music`. Planning and control: `gemini-media:estimate_cost`, `gemini-media:list_models`, `gemini-media:get_usage`, `gemini-media:get_config` (output directory, budgets). Every result includes a `path` and a `gemini-media://` `uri`; pass the `uri` of one asset as the input of the next (`image`, `lastFrame`, `referenceImages`, `images`).

## Plan template

Write the plan before generating anything; share it when a user is present.

```markdown
Project: 20 s vertical promo for the Aurora desk lamp (9:16, Reels/TikTok)
Look: warm minimal Scandinavian interior, soft morning light, oak and linen, muted palette

| # | Shot (dur) | Visual | Mode / model | Keyframe | Audio in clip |
|---|------------|--------|--------------|----------|---------------|
| 1 | Hook (4 s) | Lamp switches on in a dark room | image-to-video, lite draft then fast | kf1 | ambient only |
| 2 | Detail (8 s) | Slow orbit around the lamp head | ingredients (lamp sheet), fast, 8 s required | lamp sheet | ambient only |
| 3 | Lifestyle (6 s) | Person reading, lamp dims warmly | first+last frame, fast, 720p | kf3a, kf3b | ambient only |
| 4 | End card (2 s) | Logo + "aurora.design" | still card (ffmpeg) | card | none |

Voiceover (gemini-speech, Sulafat, "warm, intimate, unhurried"): ~45 words.
Music (gemini-music, clip, instrumental, 90 BPM soft electronic, sparse under voice).
Assembly: concat, VO ducked over music, Veo ambience at 30 %, loudness -14 LUFS.
```

Timing rules of thumb: Veo shots are 4, 6 or 8 s (8 s for 1080p/4k or reference images); voiceover runs about 2.5 words per second; a music `clip` is exactly 30 s, so trim it, or use `full` with `durationSeconds` for longer pieces. Plan the voiceover to end about a second before the picture does.

## Budget plan (before any generation)

1. For each asset in the plan call `gemini-media:estimate_cost` with its real parameters: images (`mediaType: "image"`, `model`, `imageSize`, `count`, `inputImages`), video (`mediaType: "video"`, `model`, `resolution`, `durationSeconds`, `count`), speech (`mediaType: "speech"`, `text`), music (`mediaType: "music"`, `model`). Use `compare: true` on the video line to show cheaper tiers.
2. Add a margin for iteration: a second take on about half the video shots and two or three extra keyframes.
3. Present a short table (asset, parameters, estimate) with the total, and ask for approval. Offer a draft path (`nb2` 1K, `lite` 720p, `clip`) and a final path (`fast` or `standard`, 1080p) with their totals.
4. Check `budgetRemaining` from `estimate_cost` or `gemini-media:get_usage`; if the plan exceeds it, cut scope before starting.
5. Individual calls above the approval threshold fail with `[confirmation]`. Once the user has approved the plan, retry those calls with `approvedCostUsd` set to the per-call amount they approved. Never set it without a human's approval.

Prices change; never hardcode them in the plan. Only the estimates from the tool count.

## Consistency across shots

- **Aspect ratio:** pick 16:9 or 9:16 once (Veo supports only these) and use it for every keyframe, card and clip.
- **Character or product sheet:** generate one clean reference (neutral pose, plain background) with **gemini-image**. Write a one-sentence "bible" (appearance, outfit, colors) and paste it verbatim into every prompt.
- **Keyframes:** generate each shot's first frame with the sheet in `referenceImages`, then animate it with `image`. For controlled moves, also generate the last frame (edit the first frame) and pass both `image` and `lastFrame`.
- **Ingredients:** on `fast`/`standard`, pass up to 3 references (character, product, prop) as `referenceImages` for 8 s shots with no keyframe.
- **Style:** repeat the same lighting, palette and lens words in every image and video prompt; reuse an approved frame as a style reference.
- **Continuity between shots:** extend an `omni` clip (up to 40 s, keeps characters and audio coherent) or a `fast`/`standard` clip with `gemini-media:extend_video`, or grab the last frame of a clip with ffmpeg and use it as the next shot's `image`.
- **Fixes without reshooting:** change a finished clip with `gemini-media:edit_video` (Omni: "make the sky stormy, keep everything else the same") instead of regenerating the whole shot.

## Running the video jobs

- Start all approved shots with `gemini-media:generate_video` (each returns a `jobId` immediately), then loop over the jobs calling `gemini-media:get_video` with `waitSeconds: 45` until each is `completed`, `failed` or `filtered`. Veo usually takes 1-3 minutes per job, Omni 1-5.
- Draft first: animate with `lite` 720p to check motion and timing, then re-run only the approved shots on `fast`/`standard`.
- Handle one failure at a time: fix the prompt of a filtered or failed shot and restart only that shot. Never resubmit a job that is still `working`.
- `gemini-media:get_usage` lists running jobs if you lose track of a `jobId`.

## Audio strategy

Veo and Omni add a soundtrack to every clip. Decide per project:

- **Voiceover and music carry the piece** (most promos and explainers): prompt shots for "natural ambient sound only", then keep that ambience low under the mix or drop it.
- **In-scene dialogue** (story videos): write short lines into the shot prompts (see **gemini-video**) and keep the clip audio up; add music softly.
- **Podcast or audio-only:** speech plus music, no video.

Voiceover: one `gemini-media:generate_speech` call per paragraph or scene keeps retakes cheap; keep voice and `style` identical across parts. Music: `instrumental: true`, described as "sparse, leaves room for voiceover".

## Assembly

1. Check for a shell and `ffmpeg -version`. If either is missing, skip to step 4.
2. Follow [references/ffmpeg-recipes.md](references/ffmpeg-recipes.md): join the clips (stream copy for same-format Veo clips, re-encode when mixing sources or cards), add fades or crossfades, mix voiceover over the music with ducking, lay the mix under the video, normalize loudness.
3. Review: extract a contact sheet or a few frames and inspect them if you can view images; check durations. Tell the user what you could and could not verify.
4. Without ffmpeg, deliver an edit decision list: the ordered asset paths with in/out times, where the voiceover starts, music level and fades, so the user can assemble it in any editor.

## Cost and approvals

- Default to cheap tiers for everything exploratory; spend on finals only after the look and timing are approved.
- Get approval for the plan total before generating; re-estimate and re-ask if the plan grows (extra shots, higher resolution, extensions).
- `[confirmation]`: ask, then retry with `approvedCostUsd`. `[budget]`: stop, report `gemini-media:get_usage`, propose cuts.
- Never loop retries on errors that cost money. Track spend with `get_usage` (`period: "session"`) and include it in the final report.

## Errors

Tool errors read `[kind] message` then `Hint: ...`. Each media skill has a fuller table; in short:

- `invalid`: fix the parameter the message names (Veo duration/resolution rules are the usual cause).
- `safety`, or a `filtered` video job: rephrase that asset only.
- `quota`, `unavailable`: the server already retried; wait and retry once.
- `timeout`: the work may still complete and bill, so retry once at most; for a video job, poll `gemini-media:get_video` again instead of restarting.
- `not_found`: retired model, unknown `jobId` or missing input file; check `gemini-media:list_models` or the path.
- `auth`, `permission`: stop and ask the user to run `gemini-media-mcp doctor`.
- `budget`, `confirmation`: see Cost and approvals.

## Interaction mode

- **With a user present:** confirm the brief and the plan in one message (shot list, script, music cue, budget) and get approval before spending. Show keyframes before animating them. At the end offer targeted next steps (reshoot shot 3, alternate voice, 16:9 cut).
- **Autonomous (nobody to ask):** do not block on questions. Make a compact plan, choose the draft path (`nb2`, `lite` 720p, `tts`, `clip`), keep every call below the approval threshold, and stay within the configured budget (`gemini-media:get_config`). Produce the full draft cut, then report: plan, asset paths, final file, total spend, and what the final-quality version would cost to produce.

## Final report

End every project with a compact report, whether or not a user is watching:

```markdown
Deliverable: final/promo.mp4 (20.1 s, 9:16) - or "not assembled: edit list below"
Assets: kf1.png, lamp-sheet.png, shot1.mp4 ... vo.wav, music.mp3 (paths or gemini-media:// URIs)
Models: nb2 1K, veo lite 720p (drafts), fast 720p (finals), tts Sulafat, clip
Spend: $X.XX this session (get_usage); estimate was $Y.YY
Not verified: audio mix by ear, motion in shot 3 (checked stills only)
Next options: 1080p finals (~$Z from estimate_cost), alternate voice, 16:9 cut
```

## References

- [references/ffmpeg-recipes.md](references/ffmpeg-recipes.md): tested ffmpeg commands for frames, joining, crossfades, cards, ducking, mixing, loudness and captions.
