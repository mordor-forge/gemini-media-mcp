# gemini-media extension

The `gemini-media` MCP server generates images (Nano Banana), video (Veo and
Gemini Omni, which also edits clips), speech (Gemini TTS) and music (Lyria). Skills in this extension (gemini-image,
gemini-video, gemini-speech, gemini-music, gemini-media-production,
gemini-upscale) hold the detailed prompting guides; activate the matching one
before generating. Upscaling past 4K uses `tile_image`, `edit_image` per tile
and `stitch_tiles` (gemini-upscale).

- Every call costs real money and reports its estimated cost. Use
  `estimate_cost` before expensive requests (4K images, 1080p/4k or
  standard-tier video) and ask the user before exceeding their budget. If a call
  is rejected for confirmation, ask, then retry with `approvedCostUsd`.
- Video is asynchronous: `generate_video`, `edit_video` and `extend_video`
  return a jobId; poll `get_video` until the state is `completed` (Veo takes
  1-3 minutes, Omni 1-5).
- Chain outputs by passing a previous result's `uri` or path as an input image.
- If calls fail with auth errors, call `get_config`, then tell the user to set
  the extension's API key (`gemini extensions config gemini-media-mcp`) or run
  `gemini-media-mcp configure --api-key-stdin` (for a release install, run
  `./gemini-media-mcp configure --api-key-stdin` in
  `~/.gemini/extensions/gemini-media-mcp/`).
