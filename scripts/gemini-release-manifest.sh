#!/bin/sh
# Writes .build/gemini-extension/gemini-extension.json for the Gemini CLI
# release archives (GoReleaser before hook).
#
# The repository's gemini-extension.json runs gemini-media-mcp from PATH, for
# installs from git (--ref). The release archives bundle the binary next to the
# manifest, so their copy runs it from the extension directory and needs no
# separate install.
set -eu
cd "$(dirname "$0")/.."

out=.build/gemini-extension
mkdir -p "$out"
jq '.mcpServers["gemini-media"] = {"command": "${extensionPath}${/}gemini-media-mcp", "args": []}' \
  gemini-extension.json >"$out/gemini-extension.json.tmp"
mv "$out/gemini-extension.json.tmp" "$out/gemini-extension.json"
