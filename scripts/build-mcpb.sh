#!/usr/bin/env bash
# Builds the MCP Bundle (.mcpb) for Claude Desktop and other MCPB hosts from
# GoReleaser's dist/ directory (see mcpb/README.md).
#
#   <out>/gemini-media-mcp-<version>.mcpb          the bundle
#   <out>/gemini-media-mcp-<version>.mcpb.sha256   its SHA-256 (for server.json fileSha256)
#
# Usage: scripts/build-mcpb.sh [--dist dist] [--out dist] [--version X.Y.Z] [--static-tools]
#   --static-tools  keep the tools list from mcpb/manifest.json instead of asking the
#                   freshly built server (needed when no binary runs on this machine)
#
# Env: MCPB_CLI (default @anthropic-ai/mcpb@2.1.2) - the npm package used to pack.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
dist="$repo_root/dist"
out=""
version=""
static_tools=false
mcpb_cli=${MCPB_CLI:-@anthropic-ai/mcpb@2.1.2}

while [ $# -gt 0 ]; do
  case "$1" in
    --dist) dist=$2; shift 2 ;;
    --out) out=$2; shift 2 ;;
    --version) version=$2; shift 2 ;;
    --static-tools) static_tools=true; shift ;;
    -h | --help) sed -n '2,14p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
out=${out:-$dist}

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
[ -f "$dist/artifacts.json" ] || { echo "$dist/artifacts.json not found; run goreleaser first" >&2; exit 1; }
if [ -z "$version" ]; then
  version=$(jq -r '.version' "$dist/metadata.json")
fi
version=${version#v}

# binary <goos> <goarch> <build id> -> absolute path from GoReleaser's metadata
binary() {
  local path
  path=$(jq -r --arg goos "$1" --arg goarch "$2" --arg id "$3" '
    [.[] | select(.goos == $goos and .goarch == $goarch and .extra.ID == $id
                  and (.type == "Binary" or .type == "Universal Binary"))][0].path // empty' \
    "$dist/artifacts.json")
  [ -n "$path" ] || { echo "no $1/$2 ($3) binary in $dist/artifacts.json" >&2; exit 1; }
  case "$path" in /*) echo "$path" ;; *) echo "$repo_root/$path" ;; esac
}

stage="$dist/mcpb/gemini-media-mcp"
rm -rf "$stage"
mkdir -p "$stage/server/darwin" "$stage/server/linux" "$stage/server/linux-x64" \
  "$stage/server/linux-arm64" "$stage/server/win32-x64"

install -m 0755 "$(binary darwin all darwin-universal)" "$stage/server/darwin/gemini-media-mcp"
install -m 0755 "$(binary linux amd64 gemini-media-mcp)" "$stage/server/linux-x64/gemini-media-mcp"
install -m 0755 "$(binary linux arm64 gemini-media-mcp)" "$stage/server/linux-arm64/gemini-media-mcp"
install -m 0755 "$(binary windows amd64 gemini-media-mcp)" "$stage/server/win32-x64/gemini-media-mcp.exe"
install -m 0644 "$repo_root/mcpb/launch.sh" "$stage/server/linux/launch.sh"
install -m 0644 "$repo_root/LICENSE" "$stage/LICENSE"

# Ask the server for its tools so the bundle never advertises stale ones.
tools_json=""
if [ "$static_tools" = false ]; then
  case "$(uname -s)/$(uname -m)" in
    Linux/x86_64 | Linux/amd64) host="$stage/server/linux-x64/gemini-media-mcp" ;;
    Linux/aarch64 | Linux/arm64) host="$stage/server/linux-arm64/gemini-media-mcp" ;;
    Darwin/*) host="$stage/server/darwin/gemini-media-mcp" ;;
    *) echo "cannot run a bundled binary on $(uname -s)/$(uname -m); use --static-tools" >&2; exit 1 ;;
  esac
  scratch=$(mktemp -d)
  trap 'rm -rf "$scratch"' EXIT
  # No credentials needed: the server starts without them and still lists its tools.
  {
    printf '%s\n' \
      '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"build-mcpb","version":"1"}}}' \
      '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
      '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
    sleep 3
  } | env -i PATH="$PATH" HOME="$scratch" XDG_CONFIG_HOME="$scratch/config" XDG_STATE_HOME="$scratch/state" \
    MEDIA_OUTPUT_DIR="$scratch/out" "$host" >"$scratch/stdout" 2>"$scratch/stderr" || true
  tools_json=$(jq -c 'select(.id == 2) | [.result.tools[] | {name, description: (.description | split(". ")[0] | rtrimstr("."))}]' \
    "$scratch/stdout" 2>/dev/null || true)
  if [ -z "$tools_json" ] || [ "$tools_json" = "[]" ] || [ "$tools_json" = "null" ]; then
    echo "the server did not list any tools; stderr:" >&2
    cat "$scratch/stderr" >&2
    exit 1
  fi
fi

jq --arg v "$version" --argjson tools "${tools_json:-null}" '
  .version = $v
  | if $tools == null then . else .tools = $tools end' \
  "$repo_root/mcpb/manifest.json" >"$stage/manifest.json"

bundle="$out/gemini-media-mcp-$version.mcpb"
mkdir -p "$out"
rm -f "$bundle"
npx -y "$mcpb_cli" pack "$stage" "$bundle" >&2

if command -v sha256sum >/dev/null; then
  sha=$(sha256sum "$bundle" | cut -d' ' -f1)
else
  sha=$(shasum -a 256 "$bundle" | cut -d' ' -f1)
fi
echo "$sha" >"$bundle.sha256"
echo "built $bundle (sha256 $sha)" >&2
echo "$bundle"
