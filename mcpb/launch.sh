#!/bin/sh
# Linux launcher for the gemini-media-mcp MCP Bundle (.mcpb).
#
# MCPB manifests can vary the server command per OS but not per CPU
# architecture (modelcontextprotocol/mcpb#10), so on Linux the manifest runs
# `/bin/sh server/linux/launch.sh`, and this script picks the binary for the
# machine. macOS uses a universal binary and Windows the amd64 build (Windows
# on ARM runs it under emulation), so neither needs a launcher.
#
# The script is run as an argument to /bin/sh, so it only needs to be readable:
# hosts that drop execute bits when unpacking (modelcontextprotocol/mcpb#294)
# cannot stop it, and it restores the bit on the binary itself.
#
# stdout is the MCP JSON-RPC channel: diagnostics go to stderr only.
set -eu
unset CDPATH

server_dir=$(cd -- "$(dirname -- "$0")/.." && pwd)

machine=$(uname -m)
case "$machine" in
  x86_64 | amd64) arch=x64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *)
    echo "gemini-media-mcp: unsupported CPU architecture '$machine' (supported: x86_64, aarch64)" >&2
    exit 1
    ;;
esac

bin="$server_dir/linux-$arch/gemini-media-mcp"
if [ ! -f "$bin" ]; then
  echo "gemini-media-mcp: $bin is missing from this bundle; reinstall the extension" >&2
  exit 1
fi

if [ ! -x "$bin" ]; then
  chmod +x "$bin" 2>/dev/null || {
    echo "gemini-media-mcp: $bin is not executable and chmod failed" >&2
    exit 1
  }
fi

exec "$bin" "$@"
