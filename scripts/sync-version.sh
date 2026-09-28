#!/usr/bin/env bash
# Stamps one version into every packaging manifest. Idempotent: files whose
# content would not change are left untouched.
#
# Usage: scripts/sync-version.sh 1.2.3        (a leading "v" is accepted)
#
# Updates:
#   plugin.json, .claude-plugin/plugin.json, .codex-plugin/plugin.json,
#   gemini-extension.json, mcpb/manifest.json          "version"
#   mcp.json, .claude-plugin/plugin.json, .codex-plugin/plugin.json,
#   gemini-extension.json                              npx pin "gemini-media-mcp@<version>"
#   npm/gemini-media-mcp/package.json                  "version" + optionalDependencies
#   server.json                                        version, npm version, OCI tag, MCPB URL
#   skills/*/SKILL.md                                  frontmatter metadata.version (if present)
#
# Then: scripts/validate-packaging.sh, commit, tag v<version>, push the tag.
# shellcheck disable=SC2016 # jq programs use $v in single quotes on purpose
set -euo pipefail

if [ $# -ne 1 ]; then
  sed -n '2,16p' "$0" >&2
  exit 2
fi
version=${1#v}
semver='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
if ! [[ $version =~ $semver ]]; then
  echo "not a semantic version: $1" >&2
  exit 2
fi
command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

changed=()

# replace_if_changed <file> <new-content-file>
replace_if_changed() {
  if cmp -s "$1" "$2"; then
    rm -f "$2"
  else
    cat "$2" >"$1" # keep the original file's mode
    rm -f "$2"
    changed+=("$1")
  fi
}

# stamp_json <file> <jq filter>   ($v is the version)
stamp_json() {
  local file=$1 filter=$2 tmp
  [ -f "$file" ] || { echo "missing $file" >&2; exit 1; }
  tmp=$(mktemp)
  jq --indent 2 --arg v "$version" "$filter" "$file" >"$tmp"
  replace_if_changed "$file" "$tmp"
}

# Rewrites every "gemini-media-mcp@<anything>" string (npx arguments).
pin='walk(if type == "string" and test("^gemini-media-mcp@") then "gemini-media-mcp@" + $v else . end)'

stamp_json plugin.json '.version = $v'
stamp_json mcp.json "$pin"
stamp_json .claude-plugin/plugin.json ".version = \$v | $pin"
stamp_json .codex-plugin/plugin.json ".version = \$v | $pin"
stamp_json gemini-extension.json ".version = \$v | $pin"
stamp_json mcpb/manifest.json '.version = $v'
stamp_json npm/gemini-media-mcp/package.json '.version = $v | .optionalDependencies |= with_entries(.value = $v)'
stamp_json server.json '
  .version = $v
  | .packages |= map(
      if .registryType == "npm" then .version = $v
      elif .registryType == "oci" then .identifier |= sub(":[^:/]+$"; ":" + $v)
      elif .registryType == "mcpb" then
        .identifier = "https://github.com/mordor-forge/gemini-media-mcp/releases/download/v\($v)/gemini-media-mcp-\($v).mcpb"
        | .version = $v
      else . end)'

# Skills: only the first frontmatter block, only an indented `version:` under `metadata:`.
for skill in skills/*/SKILL.md; do
  [ -f "$skill" ] || continue
  tmp=$(mktemp)
  awk -v v="$version" '
    NR == 1 && $0 == "---" { fm = 1; print; next }
    fm && $0 == "---"      { fm = 0; meta = 0; print; next }
    fm && /^metadata:[[:space:]]*$/ { meta = 1; print; next }
    fm && meta && /^[^[:space:]]/   { meta = 0 }
    fm && meta && /^[[:space:]]+version:/ {
      match($0, /^[[:space:]]+/)
      print substr($0, 1, RLENGTH) "version: \"" v "\""
      next
    }
    { print }
  ' "$skill" >"$tmp"
  replace_if_changed "$skill" "$tmp"
done

if [ ${#changed[@]} -eq 0 ]; then
  echo "all manifests already at $version" >&2
else
  echo "stamped $version into:" >&2
  printf '  %s\n' "${changed[@]}" >&2
fi
