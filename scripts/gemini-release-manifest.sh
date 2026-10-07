#!/bin/sh
# Writes .build/gemini-extension/gemini-extension.json for the Gemini CLI
# release archives (GoReleaser before hook).
#
# The repository's gemini-extension.json launches the server with npx, which
# works for every install method (Gemini CLI always ships with Node.js). The
# release archives bundle the binary next to the manifest, so their copy runs
# it directly: faster start-up and no npm download.
set -eu
cd "$(dirname "$0")/.."

out=.build/gemini-extension
mkdir -p "$out"
jq '.mcpServers["gemini-media"] = {"command": "${extensionPath}${/}gemini-media-mcp", "args": []}' \
  gemini-extension.json >"$out/gemini-extension.json.tmp"
mv "$out/gemini-extension.json.tmp" "$out/gemini-extension.json"
