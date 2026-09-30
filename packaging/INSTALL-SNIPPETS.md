# Install snippets

Copy-paste setup for each MCP client. Every snippet starts the server with the
npm launcher, `npx -y gemini-media-mcp@latest`, which needs Node.js 18+ and
downloads the native binary for your platform on first use. Replace `@latest`
with a version (for example `@1.0.0`) for a reproducible setup.

The MCP server is called `gemini-media`; its tools are `generate_image`,
`edit_image`, `generate_video`, `get_video`, `extend_video`, `generate_speech`,
`generate_music`, `list_models`, `estimate_cost`, `get_usage` and `get_config`.

## 1. Credentials (once per machine)

Many clients do not pass your shell environment to MCP servers (Gemini CLI,
Codex, Claude Desktop, most GUI editors), so the portable way is to save the key
in the server's own config file:

```sh
# Paste your key from https://aistudio.google.com/apikey and press Enter
# (reading it from stdin keeps it out of your shell history).
npx -y gemini-media-mcp configure --api-key-stdin
npx -y gemini-media-mcp doctor       # verifies the key and model access
```

This writes `config.yaml` with mode 0600 to `~/.config/gemini-media-mcp/`
(Linux), `~/Library/Application Support/gemini-media-mcp/` (macOS) or
`%AppData%\gemini-media-mcp\` (Windows). Other options:

- **Vertex AI:** `gcloud auth application-default login`, then
  `npx -y gemini-media-mcp configure --backend vertex --project MY_PROJECT --location global`.
- **Budget:** add `--daily-budget-usd 5` to refuse generations past $5/day.
- **Environment variables** override the file: `GEMINI_MEDIA_API_KEY` >
  `GOOGLE_API_KEY` > `GEMINI_API_KEY`; `GOOGLE_CLOUD_PROJECT`,
  `GOOGLE_CLOUD_LOCATION`, `MEDIA_OUTPUT_DIR` (default `~/generated_media`).

## Claude Code

Plugin (MCP server + skills):

```sh
claude plugin marketplace add mordor-forge/gemini-media-mcp
claude plugin install gemini-media@mordor-forge
```

Installing from inside a session (`/plugin install gemini-media@mordor-forge`)
opens a dialog for the API key (kept in your keychain), output directory and
optional daily budget; reopen it with `/plugin configure gemini-media@mordor-forge`.
Leave the key empty to use `GEMINI_API_KEY` from your environment or the
`configure` file.

MCP server only (Claude Code passes your environment through):

```sh
claude mcp add --scope user gemini-media -- npx -y gemini-media-mcp@latest
```

## Codex

Plugin (MCP server + skills; forwards `GEMINI_API_KEY`, `GOOGLE_*` and
`GEMINI_MEDIA_*` from Codex's environment):

```sh
codex plugin marketplace add mordor-forge/gemini-media-mcp
codex plugin add gemini-media@mordor-forge
```

MCP server only, in `~/.codex/config.toml`:

```toml
[mcp_servers.gemini-media]
command = "npx"
args = ["-y", "gemini-media-mcp@latest"]
env_vars = ["GEMINI_API_KEY"]   # forward from your shell; omit if you used `configure`
startup_timeout_sec = 60        # the first run downloads the binary
tool_timeout_sec = 300          # 4K images and video polls can exceed the 60 s default
```

## Gemini CLI

```sh
gemini extensions install https://github.com/mordor-forge/gemini-media-mcp
gemini extensions config gemini-media-mcp    # API key (keychain), Vertex project/location, output dir
```

The extension installs from the latest GitHub release (native binary, no npm
download); `--ref main` installs from git and starts the server with npx.
Gemini CLI never passes your shell's `GEMINI_API_KEY` or `GOOGLE_CLOUD_PROJECT`
to extensions, so set them with `gemini extensions config` or use `configure`.
Stdio MCP servers only start in trusted folders.

## VS Code (GitHub Copilot)

Agent plugin (MCP server + skills): run **Chat: Install Plugin From Source** with
`https://github.com/mordor-forge/gemini-media-mcp`, or add the marketplace to
your settings and install from the Extensions view (`@agentPlugins`):

```json
"chat.plugins.marketplaces": ["mordor-forge/gemini-media-mcp"]
```

MCP server only:

```sh
code --add-mcp '{"name":"gemini-media","command":"npx","args":["-y","gemini-media-mcp@latest"]}'
```

Install link: `vscode:mcp/install?%7B%22name%22%3A%22gemini-media%22%2C%22type%22%3A%22stdio%22%2C%22command%22%3A%22npx%22%2C%22args%22%3A%5B%22-y%22%2C%22gemini-media-mcp%40latest%22%5D%7D`

Or `.vscode/mcp.json`, prompting once for the key and storing it securely:

```json
{
  "inputs": [
    { "type": "promptString", "id": "gemini-api-key", "description": "Gemini API key", "password": true }
  ],
  "servers": {
    "gemini-media": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "gemini-media-mcp@latest"],
      "env": { "GEMINI_MEDIA_API_KEY": "${input:gemini-api-key}" }
    }
  }
}
```

## Cursor

[Add to Cursor](cursor://anysphere.cursor-deeplink/mcp/install?name=gemini-media&config=eyJjb21tYW5kIjoibnB4IiwiYXJncyI6WyIteSIsImdlbWluaS1tZWRpYS1tY3BAbGF0ZXN0Il19)
(`config` is the base64 of `{"command":"npx","args":["-y","gemini-media-mcp@latest"]}`),
or `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "gemini-media": { "command": "npx", "args": ["-y", "gemini-media-mcp@latest"] }
  }
}
```

Agent plugin with the skills (Cursor reads the repository's `plugin.json`):

```sh
git clone https://github.com/mordor-forge/gemini-media-mcp ~/.cursor/plugins/local/gemini-media
```

## Claude Desktop

Download `gemini-media-mcp-<version>.mcpb` from the
[latest release](https://github.com/mordor-forge/gemini-media-mcp/releases/latest)
and open it (or **Settings > Extensions > Install Extension...**). The bundle
contains native binaries (no Node.js needed) and asks for the API key or Vertex
project and the output directory.

Without the bundle, in `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "gemini-media": { "command": "npx", "args": ["-y", "gemini-media-mcp@latest"] }
  }
}
```

## OpenCode

`opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "gemini-media": {
      "type": "local",
      "command": ["npx", "-y", "gemini-media-mcp@latest"],
      "enabled": true,
      "environment": { "GEMINI_API_KEY": "{env:GEMINI_API_KEY}" }
    }
  }
}
```

## Goose

[Install in Goose](goose://extension?cmd=npx&arg=-y&arg=gemini-media-mcp%40latest&timeout=300&id=gemini-media&name=Gemini%20Media&description=Images%2C%20video%2C%20speech%20and%20music%20with%20Google%20Gemini%20models),
or `goose configure` > **Add Extension** > **Command-line Extension** with
command `npx -y gemini-media-mcp@latest` and timeout 300. In
`~/.config/goose/config.yaml`:

```yaml
extensions:
  gemini-media:
    type: stdio
    name: gemini-media
    enabled: true
    cmd: npx
    args: ["-y", "gemini-media-mcp@latest"]
    envs: {}
    env_keys: []
    timeout: 300
```

## Zed

`settings.json`:

```json
{
  "context_servers": {
    "gemini-media": {
      "command": "npx",
      "args": ["-y", "gemini-media-mcp@latest"],
      "env": {}
    }
  }
}
```

## Windsurf

`~/.codeium/windsurf/mcp_config.json`:

```json
{
  "mcpServers": {
    "gemini-media": { "command": "npx", "args": ["-y", "gemini-media-mcp@latest"] }
  }
}
```

## Docker

The image (`ghcr.io/mordor-forge/gemini-media-mcp`, linux/amd64 + arm64) saves
media to `/output` and its spend ledger to `/state`. File paths in tool results
are container paths: `/output/...` is the host directory you mount. Mount any
input images too (for example `-v "$PWD:/work:ro"`, then refer to `/work/...`).
On Linux add `--user <uid>:<gid>` (your `id -u`/`id -g`) so the generated
files belong to you.

stdio, as an MCP client entry:

```json
{
  "mcpServers": {
    "gemini-media": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "-e", "GEMINI_API_KEY",
               "-v", "/home/you/generated_media:/output",
               "-v", "gemini-media-state:/state",
               "ghcr.io/mordor-forge/gemini-media-mcp:latest"],
      "env": { "GEMINI_API_KEY": "your-key" }
    }
  }
}
```

Streamable HTTP (one shared server; a bearer token is mandatory off loopback):

```sh
TOKEN=$(openssl rand -hex 32)
docker run -d --name gemini-media -p 127.0.0.1:8765:8765 \
  -e GEMINI_API_KEY \
  -e GEMINI_MEDIA_TRANSPORT=http -e GEMINI_MEDIA_HTTP_ADDR=0.0.0.0:8765 \
  -e GEMINI_MEDIA_HTTP_TOKEN="$TOKEN" \
  -v "$HOME/generated_media:/output" -v gemini-media-state:/state \
  ghcr.io/mordor-forge/gemini-media-mcp:latest

claude mcp add --transport http gemini-media http://localhost:8765/mcp \
  --header "Authorization: Bearer $TOKEN"
```

Over HTTP the server reads input files only from its output directory and the
directories listed in `GEMINI_MEDIA_INPUT_DIRS` (inline data URIs always work).

## Skills only

The skills (`gemini-image`, `gemini-video`, `gemini-speech`, `gemini-music`,
`gemini-media-production`) teach an agent how to prompt and budget each medium.
Plugins above already include them; for any other agent:

```sh
npx skills add mordor-forge/gemini-media-mcp
```

They drive the `gemini-media` MCP server, so configure it as well.

## Without Node.js

Download an archive from the
[releases page](https://github.com/mordor-forge/gemini-media-mcp/releases) or
`go install github.com/mordor-forge/gemini-media-mcp/cmd/gemini-media-mcp@latest`,
then use `"command": "gemini-media-mcp"` (no args) in any snippet above. The npm
launcher also picks up a `gemini-media-mcp` binary on `PATH`, or the one named
by `GEMINI_MEDIA_MCP_BINARY`.
