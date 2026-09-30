# ffmpeg recipes

Assembly commands for gemini-media outputs. Tested with ffmpeg 7; they use only built-in filters and should work on ffmpeg 5 or later. Replace file names with the `path` values from tool results. Write outputs to a separate folder (`mkdir -p final`) so generated sources stay untouched.

## Contents

- [Check the environment and inputs](#check-the-environment-and-inputs)
- [Review frames](#review-frames): first/last frame, contact sheet
- [Join clips](#join-clips): same-format concat, mixed formats, crossfades
- [Trim, fade, reframe](#trim-fade-reframe)
- [Stills and title cards](#stills-and-title-cards)
- [Audio](#audio): replace, music under video, voiceover over music with ducking, full three-layer mix, podcast intro, join speech parts, loop and fade music, loudness
- [Captions](#captions)
- [Format conversions](#format-conversions)

## Check the environment and inputs

```bash
ffmpeg -version | head -1            # is ffmpeg installed?
ffprobe -v error -show_entries format=duration -of csv=p=0 shot1.mp4
ffprobe -v error -show_entries stream=codec_type,width,height,r_frame_rate,sample_rate,channels -of compact shot1.mp4
```

Without `ffprobe`, `ffmpeg -i file` prints duration and streams (it exits with an error because no output is given; that is expected).

Typical inputs: Veo clips are 24 fps MP4 with an audio track; speech is 24 kHz mono 16-bit WAV; music is 44.1 kHz stereo MP3 (or WAV). The recipes resample everything to 48 kHz stereo before mixing.

## Review frames

Agents that can view images but not video can check a clip through stills.

```bash
# First and last frame (the last frame can seed the next shot's `image`)
ffmpeg -y -i shot1.mp4 -frames:v 1 shot1_first.png
ffmpeg -y -sseof -1 -i shot1.mp4 -update 1 shot1_last.png

# Contact sheet: one frame per second of an 8 s clip in a 4x2 grid
ffmpeg -y -i shot1.mp4 -vf "fps=1,scale=480:-1,tile=4x2" -frames:v 1 shot1_sheet.jpg
```

## Join clips

### Same format (clips from the same Veo model, resolution and ratio)

Fast, no re-encode:

```bash
printf "file '%s'\n" "$PWD/shot1.mp4" "$PWD/shot2.mp4" "$PWD/shot3.mp4" > list.txt
ffmpeg -y -f concat -safe 0 -i list.txt -c copy final/joined.mp4
```

If ffmpeg warns about non-monotonic timestamps, the inputs differ: use the next recipe.

### Mixed resolutions or sources (re-encode)

Scale everything to one canvas (here 1920x1080; use 1280x720, or 1080x1920 for vertical):

```bash
ffmpeg -y -i shot1.mp4 -i shot2.mp4 -filter_complex \
"[0:v]scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=24,format=yuv420p[v0];\
[1:v]scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=24,format=yuv420p[v1];\
[0:a]aresample=48000[a0];[1:a]aresample=48000[a1];\
[v0][a0][v1][a1]concat=n=2:v=1:a=1[v][a]" \
-map "[v]" -map "[a]" -c:v libx264 -crf 18 -preset medium -c:a aac -b:a 192k final/joined.mp4
```

Every input needs an audio track for `a=1`. Give a silent clip (for example a Vertex AI clip made with `generateAudio: false`) a silent track first:

```bash
ffmpeg -y -i silent.mp4 -f lavfi -i anullsrc=r=48000:cl=stereo -map 0:v -map 1:a -c:v copy -c:a aac -shortest silent_fixed.mp4
```

### Crossfade two clips

`offset` = first clip duration minus the fade duration (8 s clip, 0.5 s fade: 7.5). Clips must share resolution and frame rate.

```bash
ffmpeg -y -i shot1.mp4 -i shot2.mp4 -filter_complex \
"[0:v][1:v]xfade=transition=fade:duration=0.5:offset=7.5,format=yuv420p[v];[0:a][1:a]acrossfade=d=0.5[a]" \
-map "[v]" -map "[a]" -c:v libx264 -crf 18 -c:a aac final/xfade.mp4
```

Other transitions: `dissolve`, `wipeleft`, `slideup`, `circleopen`, `fadeblack`.

## Trim, fade, reframe

```bash
# Frame-accurate trim: start at 0.5 s, keep 6 s (re-encodes)
ffmpeg -y -ss 0.5 -i shot1.mp4 -t 6 -c:v libx264 -crf 18 -c:a aac shot1_trim.mp4

# Fade video and audio in and out (16 s total)
ffmpeg -y -i joined.mp4 -vf "fade=t=in:st=0:d=0.5,fade=t=out:st=15.5:d=0.5" \
-af "afade=t=in:d=0.5,afade=t=out:st=15.5:d=0.5" -c:v libx264 -crf 18 -c:a aac final/faded.mp4

# Center-crop 16:9 to 9:16 (prefer generating natively at 9:16)
ffmpeg -y -i shot1.mp4 -vf "crop=ih*9/16:ih,scale=720:1280,setsar=1" -c:v libx264 -crf 18 -c:a copy shot1_vertical.mp4
```

## Stills and title cards

```bash
# 3 s card from an image, with a silent audio track so it concatenates cleanly
ffmpeg -y -loop 1 -framerate 24 -i card.png -f lavfi -i anullsrc=r=48000:cl=stereo -t 3 \
-vf "scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,setsar=1,format=yuv420p" \
-c:v libx264 -crf 18 -c:a aac -shortest card.mp4

# Slow push-in (Ken Burns) on a still, 5 s
ffmpeg -y -loop 1 -i still.png -f lavfi -i anullsrc=r=48000:cl=stereo \
-vf "scale=2560:-2,zoompan=z='min(zoom+0.0015,1.2)':x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)':d=120:s=1280x720:fps=24,format=yuv420p" \
-t 5 -c:v libx264 -crf 18 -c:a aac -shortest still_push.mp4
```

Join cards with Veo clips using the re-encode concat recipe.

## Audio

### Replace a clip's audio with music

```bash
ffmpeg -y -i joined.mp4 -i music.mp3 -map 0:v -map 1:a -c:v copy -c:a aac -b:a 192k -shortest final/with_music.mp4
```

### Music under the clip's own sound

```bash
ffmpeg -y -i joined.mp4 -i music.mp3 -filter_complex \
"[1:a]volume=0.3,afade=t=in:d=1,afade=t=out:st=14:d=2[m];[0:a][m]amix=inputs=2:duration=first:normalize=0[a]" \
-map 0:v -map "[a]" -c:v copy -c:a aac -b:a 192k final/music_under.mp4
```

`duration=first` ends the mix with the video; set the fade-out start to video length minus fade length.

### Voiceover over music with ducking

The music dips automatically while the voice speaks. `adelay` starts the voice 2 s in; `apad` keeps the sidechain alive so the music runs to its end.

```bash
ffmpeg -y -i music.mp3 -i vo.wav -filter_complex \
"[0:a]aresample=48000,aformat=channel_layouts=stereo[m];\
[1:a]aresample=48000,aformat=channel_layouts=stereo,adelay=2000:all=1,apad,asplit=2[vo][sc];\
[m][sc]sidechaincompress=threshold=0.05:ratio=8:attack=20:release=400[duck];\
[duck][vo]amix=inputs=2:duration=first:normalize=0,afade=t=out:st=27:d=3[a]" \
-map "[a]" -c:a libmp3lame -q:a 2 final/vo_mix.mp3
```

Stronger ducking: lower `threshold` (0.02) or raise `ratio` (12). Simpler alternative with no ducking: `[0:a]volume=0.2[m]` and mix.

### Full mix onto video: ambience + music bed + voiceover

```bash
ffmpeg -y -i joined.mp4 -i music.mp3 -i vo.wav -filter_complex \
"[0:a]volume=0.3[amb];\
[1:a]aresample=48000,aformat=channel_layouts=stereo,volume=0.6[m];\
[2:a]aresample=48000,aformat=channel_layouts=stereo,adelay=1000:all=1,apad,asplit=2[vo][sc];\
[m][sc]sidechaincompress=threshold=0.05:ratio=8:attack=20:release=400[bed];\
[amb][bed][vo]amix=inputs=3:duration=first:normalize=0,afade=t=out:st=14.5:d=1.5[a]" \
-map 0:v -map "[a]" -c:v copy -c:a aac -b:a 192k -movflags +faststart final/promo.mp4
```

Set `volume=0` on `[0:a]` (or drop it) to discard Veo's own soundtrack.

### Podcast intro: music first, then voice, music ducked under it

```bash
ffmpeg -y -i music.mp3 -i vo.wav -filter_complex \
"[1:a]aresample=48000,aformat=channel_layouts=stereo,adelay=6000:all=1,apad=pad_dur=2,asplit=2[vo][sc];\
[0:a]aresample=48000,aformat=channel_layouts=stereo[m];\
[m][sc]sidechaincompress=threshold=0.05:ratio=10:attack=15:release=600[duck];\
[duck][vo]amix=inputs=2:duration=shortest:normalize=0[a]" \
-map "[a]" -c:a pcm_s16le final/intro.wav
```

The result ends 2 s after the voice. Add `afade=t=out:st=<end-2>:d=2` after `amix` for a soft tail.

### Join speech parts

```bash
# Same format (all from generate_speech): no re-encode
printf "file '%s'\n" "$PWD/part1.wav" "$PWD/part2.wav" > parts.txt
ffmpeg -y -f concat -safe 0 -i parts.txt -c copy narration.wav

# With a 0.8 s pause between two parts
ffmpeg -y -i part1.wav -i part2.wav -filter_complex "[0:a]apad=pad_dur=0.8[a0];[a0][1:a]concat=n=2:v=0:a=1[a]" -map "[a]" narration.wav
```

### Loop or extend music to a length

```bash
ffmpeg -y -stream_loop -1 -i music.mp3 -t 60 -af "afade=t=out:st=56:d=4" -c:a libmp3lame -q:a 2 music_60s.mp3
```

A hard loop point can click; for long beds prefer generating a `full` instrumental track at the needed length.

### Loudness

```bash
# Social / streaming video: about -14 LUFS; podcasts: about -16 LUFS
ffmpeg -y -i final/promo.mp4 -c:v copy -af loudnorm=I=-14:TP=-1.5:LRA=11 -ar 48000 -c:a aac -b:a 192k final/promo_norm.mp4
```

## Captions

Soft (toggleable) captions from an SRT file:

```bash
ffmpeg -y -i final/promo.mp4 -i captions.srt -map 0 -map 1 -c:v copy -c:a copy -c:s mov_text -metadata:s:s:0 language=eng final/promo_captioned.mp4
```

Burned-in captions need an ffmpeg built with libass: `-vf subtitles=captions.srt` (re-encodes the video).

## Format conversions

```bash
ffmpeg -y -i vo.wav -c:a libmp3lame -q:a 2 vo.mp3                    # WAV to MP3
ffmpeg -y -f s16le -ar 24000 -ac 1 -i speech.pcm speech.wav           # raw PCM (format: "pcm") to WAV
ffmpeg -y -i final/promo.mp4 -vf "fps=12,scale=480:-1" final/promo.gif  # quick GIF preview
```
