# gemini-media-mcp

MCP server for Google's generative media models: **Nano Banana** images, **Veo**
and **Gemini Omni** video, **Gemini TTS** speech and **Lyria** music, with cost estimates, spend
budgets and a model catalog that tracks Google's renames and retirements.

This npm package is a thin launcher for the native Go binary. npm installs only
the binary for your platform (one of the `gemini-media-mcp-<os>-<cpu>` optional
dependencies); no install scripts run.

## Use it

```sh
# 1. Save your Gemini API key once (https://aistudio.google.com/apikey).
#    Written to <user config dir>/gemini-media-mcp/config.yaml with mode 0600.
printf '%s\n' "$GEMINI_API_KEY" | npx -y gemini-media-mcp configure --api-key-stdin

# 2. Check credentials and model access.
npx -y gemini-media-mcp doctor
```

Then register the stdio server in your MCP client:

```json
{
  "mcpServers": {
    "gemini-media": { "command": "npx", "args": ["-y", "gemini-media-mcp@latest"] }
  }
}
```

Per-client instructions (Claude Code, Codex, Gemini CLI, VS Code, Cursor,
Claude Desktop, Zed, Windsurf, Goose, OpenCode, Docker):
<https://github.com/mordor-forge/gemini-media-mcp/blob/main/packaging/INSTALL-SNIPPETS.md>

## Troubleshooting

- `the platform package ... is not installed`: the install skipped optional
  dependencies (`--omit=optional`). Reinstall without it, or download a binary
  from the [releases page](https://github.com/mordor-forge/gemini-media-mcp/releases)
  and set `GEMINI_MEDIA_MCP_BINARY=/path/to/gemini-media-mcp`.
- Unsupported platform: `go install github.com/mordor-forge/gemini-media-mcp/cmd/gemini-media-mcp@latest`.

Source, docs and issues: <https://github.com/mordor-forge/gemini-media-mcp>.
License: Apache-2.0.
