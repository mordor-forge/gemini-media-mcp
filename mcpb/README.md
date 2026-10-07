# MCP Bundle (.mcpb)

Sources for the [MCP Bundle](https://github.com/modelcontextprotocol/mcpb) that
Claude Desktop (and other MCPB hosts) install with a double-click. Every release
attaches `gemini-media-mcp-<version>.mcpb`; `server.json` references it for the
MCP Registry.

| File | Purpose |
|---|---|
| `manifest.json` | Bundle manifest (MCPB 0.3). `version` is stamped by `scripts/sync-version.sh`; the `tools` list is regenerated at build time from the server itself. |
| `launch.sh` | Linux launcher: picks the x64 or arm64 binary and restores a lost execute bit. |

## How CI builds it

`.github/workflows/release.yml` runs, after `goreleaser release`:

```sh
scripts/build-mcpb.sh   # -> dist/gemini-media-mcp-<version>.mcpb (+ .sha256)
gh release upload "v<version>" dist/gemini-media-mcp-<version>.mcpb
```

`scripts/build-mcpb.sh` reads `dist/artifacts.json`, stages

```
manifest.json            version + tools filled in
LICENSE
server/darwin/gemini-media-mcp          universal (arm64 + x86_64) binary
server/linux/launch.sh                  run as `/bin/sh launch.sh`
server/linux-x64/gemini-media-mcp
server/linux-arm64/gemini-media-mcp
server/win32-x64/gemini-media-mcp.exe   Windows on ARM runs it under emulation
```

starts the Linux (or macOS) binary without credentials to collect its
`tools/list`, and packs the directory with `npx @anthropic-ai/mcpb pack`, which
also validates the manifest. The SHA-256 goes into `server.json` `fileSha256`.

Local build:

```sh
goreleaser release --snapshot --clean --skip=publish
scripts/build-mcpb.sh
```

## Design notes

- MCPB can vary the command per OS but not per CPU
  ([mcpb#10](https://github.com/modelcontextprotocol/mcpb/issues/10)), hence the
  macOS universal binary and the Linux launcher.
- Optional `user_config` fields default to `""`: hosts substitute unset values
  without a default literally (`${user_config.x}`), while the server ignores
  empty environment variables and falls back to its own defaults
  (`~/generated_media`, the `configure` config file, Vertex AI detection).
- Values are passed as `GEMINI_MEDIA_*` variables: the server gives them
  precedence over `GOOGLE_API_KEY`/`GEMINI_API_KEY`, and no other tool reads them.
- The bundle is unsigned (`mcpb sign` needs a code-signing certificate).
