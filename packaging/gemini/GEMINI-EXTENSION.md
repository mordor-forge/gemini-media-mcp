# gemini-media extension

The `gemini-media` MCP server generates images (Nano Banana), video (Veo),
speech (Gemini TTS) and music (Lyria). Skills in this extension (gemini-image,
gemini-video, gemini-speech, gemini-music, gemini-media-production) hold the
detailed prompting guides; activate the matching one before generating.

- Every call costs real money and reports its estimated cost. Use
  `estimate_cost` before expensive requests (4K images, 1080p/4k or
  standard-tier video) and ask the user before exceeding their budget. If a call
  is rejected for confirmation, ask, then retry with `approvedCostUsd`.
- Video is asynchronous: `generate_video` returns a jobId; poll `get_video`
  until the state is `completed` (Veo takes 1-3 minutes).
- Chain outputs by passing a previous result's `uri` or path as an input image.
- If calls fail with auth errors, call `get_config`, then tell the user to set
  the extension's API key (`gemini extensions config gemini-media-mcp`) or run
  `npx -y gemini-media-mcp configure --api-key-stdin`.
