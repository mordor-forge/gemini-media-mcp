#!/usr/bin/env bash
# Assembles the npm packages from GoReleaser's dist/ directory:
#
#   <out>/gemini-media-mcp-<os>-<cpu>/   one per platform: bin/<binary>, package.json with
#                                        "os"/"cpu" so npm installs only the matching one
#   <out>/gemini-media-mcp/              the launcher (npm/gemini-media-mcp), with its
#                                        optionalDependencies pinned to the same version
#   <out>/publish-order.txt              platform packages first, launcher last
#
# Usage: scripts/build-npm-packages.sh [--dist dist] [--out dist/npm] [--version X.Y.Z]
#   --version defaults to dist/metadata.json .version (GoReleaser's version, without "v").
#
# Publish (the release workflow does this):
#   while read -r dir; do npm publish --provenance --access public "$dir"; done < dist/npm/publish-order.txt
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
dist="$repo_root/dist"
out=""
version=""

while [ $# -gt 0 ]; do
  case "$1" in
    --dist) dist=$2; shift 2 ;;
    --out) out=$2; shift 2 ;;
    --version) version=$2; shift 2 ;;
    -h | --help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
out=${out:-$dist/npm}

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
[ -f "$dist/artifacts.json" ] || { echo "$dist/artifacts.json not found; run goreleaser first" >&2; exit 1; }

if [ -z "$version" ]; then
  version=$(jq -r '.version' "$dist/metadata.json")
fi
version=${version#v}
[ -n "$version" ] && [ "$version" != "null" ] || { echo "could not determine the version" >&2; exit 1; }

rm -rf "$out"
mkdir -p "$out"
: >"$out/publish-order.txt"

# npm os/cpu names -> GoReleaser goos/goarch
targets=(
  "darwin arm64 darwin arm64"
  "darwin x64 darwin amd64"
  "linux arm64 linux arm64"
  "linux x64 linux amd64"
  "win32 arm64 windows arm64"
  "win32 x64 windows amd64"
)

for target in "${targets[@]}"; do
  read -r os cpu goos goarch <<<"$target"
  pkg="gemini-media-mcp-$os-$cpu"
  exe=gemini-media-mcp
  [ "$os" = win32 ] && exe=gemini-media-mcp.exe

  src=$(jq -r --arg goos "$goos" --arg goarch "$goarch" '
    [.[] | select(.type == "Binary" and .goos == $goos and .goarch == $goarch
                  and .extra.ID == "gemini-media-mcp")][0].path // empty' "$dist/artifacts.json")
  if [ -z "$src" ]; then
    echo "no $goos/$goarch binary in $dist/artifacts.json" >&2
    exit 1
  fi
  case "$src" in /*) ;; *) src="$repo_root/$src" ;; esac

  dir="$out/$pkg"
  mkdir -p "$dir/bin"
  cp "$src" "$dir/bin/$exe"
  chmod 0755 "$dir/bin/$exe"
  cp "$repo_root/LICENSE" "$dir/LICENSE"
  sed -e "s/@@OS@@/$os/g" -e "s/@@CPU@@/$cpu/g" -e "s/@@VERSION@@/$version/g" -e "s/@@EXE@@/$exe/g" \
    "$repo_root/npm/platform-package.json.tmpl" | jq . >"$dir/package.json"
  cat >"$dir/README.md" <<EOF
# $pkg

The \`$os\`/\`$cpu\` binary of [gemini-media-mcp](https://www.npmjs.com/package/gemini-media-mcp).
Do not install this package directly: install or \`npx\` **gemini-media-mcp**, which
selects the right platform package automatically.
EOF
  echo "$dir" >>"$out/publish-order.txt"
done

main="$out/gemini-media-mcp"
mkdir -p "$main"
cp -R "$repo_root/npm/gemini-media-mcp/." "$main/"
cp "$repo_root/LICENSE" "$main/LICENSE"
chmod 0755 "$main/bin/gemini-media-mcp.js"
jq --arg v "$version" '
  .version = $v
  | .optionalDependencies |= with_entries(.value = $v)' \
  "$repo_root/npm/gemini-media-mcp/package.json" >"$main/package.json"
echo "$main" >>"$out/publish-order.txt"

echo "npm packages for $version in $out:" >&2
sed 's/^/  /' "$out/publish-order.txt" >&2
