#!/usr/bin/env bash
# Validates the multi-harness packaging: every manifest parses, versions and
# names agree, the portable formats follow their specs, and - when the tools
# are installed - the official validators pass. Missing tools are skipped.
#
# Usage: scripts/validate-packaging.sh [--expect-version X.Y.Z] [--offline] [--release]
#   --expect-version  fail unless all manifests carry this version (release CI passes the tag)
#   --offline         skip checks that download tools or call registries
#   --release         treat placeholders (e.g. the MCPB fileSha256) as errors
# shellcheck disable=SC2016 # jq programs use $vars in single quotes on purpose
set -uo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root" || exit 1

expect=""
offline=false
release=false
while [ $# -gt 0 ]; do
  case "$1" in
    --expect-version) expect=${2#v}; shift 2 ;;
    --offline) offline=true; shift ;;
    --release) release=true; shift ;;
    -h | --help) sed -n '2,9p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

errors=0
warnings=0
ok() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; errors=$((errors + 1)); }
warn() { printf 'WARN %s\n' "$*"; warnings=$((warnings + 1)); }
skip() { printf 'skip %s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }
indent() { sed 's/^/     /'; }

if ! have jq; then
  echo "jq is required" >&2
  exit 1
fi

# check <description> <jq expression that must be true> <file>
check() {
  if jq -e "$2" "$3" >/dev/null 2>&1; then ok "$1"; else fail "$1 ($3)"; fi
}

# frontmatter <file> <awk pattern> : prints matching frontmatter lines of a SKILL.md
frontmatter() {
  awk -v pat="$2" 'NR == 1 && $0 == "---" { fm = 1; next } fm && $0 == "---" { exit } fm && $0 ~ pat { print }' "$1"
}

echo "== JSON syntax"
json_files=(
  plugin.json mcp.json
  .claude-plugin/plugin.json .claude-plugin/marketplace.json
  .codex-plugin/plugin.json
  gemini-extension.json server.json mcpb/manifest.json
  glama.json
)
for f in "${json_files[@]}"; do
  if [ ! -f "$f" ]; then fail "$f is missing"; continue; fi
  if jq empty "$f" 2>/dev/null; then ok "$f parses"; else fail "$f is not valid JSON"; fi
done
[ "$errors" -eq 0 ] || { echo "fix the JSON errors first"; exit 1; }

echo "== Versions"
version=${expect:-$(jq -r .version plugin.json)}
echo "     expected version: $version"
declare -a found=()
add() { found+=("$1=$2"); }
add plugin.json "$(jq -r .version plugin.json)"
add .claude-plugin/plugin.json "$(jq -r .version .claude-plugin/plugin.json)"
add .codex-plugin/plugin.json "$(jq -r .version .codex-plugin/plugin.json)"
add gemini-extension.json "$(jq -r .version gemini-extension.json)"
add mcpb/manifest.json "$(jq -r .version mcpb/manifest.json)"
add server.json "$(jq -r .version server.json)"
add "server.json OCI tag" "$(jq -r '.packages[] | select(.registryType == "oci") | .identifier | sub("^.*:"; "")' server.json)"
add "server.json MCPB version" "$(jq -r '.packages[] | select(.registryType == "mcpb") | .version' server.json)"
add "server.json MCPB URL" "$(jq -r '.packages[] | select(.registryType == "mcpb") | .identifier
  | capture("/download/v(?<a>[^/]+)/gemini-media-mcp-(?<b>.+)\\.mcpb$") | if .a == .b then .a else "mismatch:\(.a)/\(.b)" end' server.json)"
for skill in skills/*/SKILL.md; do
  [ -f "$skill" ] || continue
  v=$(frontmatter "$skill" '^[[:space:]]+version:' | head -1 | sed -E 's/^[[:space:]]+version:[[:space:]]*"?([^"]*)"?[[:space:]]*$/\1/')
  [ -n "$v" ] && add "$skill metadata.version" "$v"
done
version_errors=0
for entry in "${found[@]}"; do
  if [ "${entry##*=}" != "$version" ]; then
    fail "${entry%=*} has version '${entry##*=}', expected '$version'"
    version_errors=$((version_errors + 1))
  fi
done
[ "$version_errors" -eq 0 ] && ok "${#found[@]} version references agree on $version (fix drift with scripts/sync-version.sh)"

echo "== Names"
plugin=$(jq -r .name plugin.json)
check "Claude plugin name matches plugin.json ($plugin)" ".name == \"$plugin\"" .claude-plugin/plugin.json
check "Codex overlay name matches plugin.json" ".name == \"$plugin\"" .codex-plugin/plugin.json
check "marketplace lists $plugin with source ./" "[.plugins[] | select(.name == \"$plugin\" and (.source == \"./\" or .source == \".\"))] | length == 1" .claude-plugin/marketplace.json
check "marketplace entry sets no version (plugin.json is authoritative)" '[.plugins[] | select(has("version"))] | length == 0' .claude-plugin/marketplace.json
for f in mcp.json .claude-plugin/plugin.json .codex-plugin/plugin.json gemini-extension.json; do
  check "$f names the MCP server gemini-media" '.mcpServers | has("gemini-media")' "$f"
  # No npm package: plugins run the release binary from PATH.
  check "$f launches gemini-media-mcp from PATH" '.mcpServers["gemini-media"] | .command == "gemini-media-mcp" and (.args // []) == []' "$f"
done
check "server.json lists no npm package" '[.packages[] | select(.registryType == "npm")] | length == 0' server.json
server_name=$(jq -r .name server.json)
if grep -q "io.modelcontextprotocol.server.name=\"$server_name\"" Dockerfile; then
  ok "Dockerfile carries the MCP registry label"
else
  fail "Dockerfile lacks LABEL io.modelcontextprotocol.server.name=\"$server_name\""
fi
gemini_name=$(jq -r .name gemini-extension.json)
if grep -q "{{ end }}\.$gemini_name\$" .goreleaser.yaml; then
  ok "GoReleaser Gemini archives end in .$gemini_name (the extension name)"
else
  fail "GoReleaser Gemini archive name must end in .$gemini_name"
fi

echo "== Agent Plugins 1.0 (plugin.json, mcp.json)"
check "plugin.json \$schema is the 1.0.0 identifier" '."$schema" == "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"' plugin.json
check "plugin.json has only spec fields" '(keys - ["$schema","name","version","description","author","homepage","repository","license","keywords","extensions"]) == []' plugin.json
check "plugin.json name follows the naming rules" '.name | test("^(?!.*(--|\\.\\.))[a-z0-9]([a-z0-9.-]*[a-z0-9])?$") and length <= 64' plugin.json
check "plugin.json author has only name/email/url" '(.author // {} | keys - ["name","email","url"]) == []' plugin.json
check "plugin.json extensions are objects" '[.extensions // {} | .[] | type] | all(. == "object")' plugin.json
check "mcp.json \$schema is the 1.0.0 identifier" '."$schema" == "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"' mcp.json
check "mcp.json has only \$schema and mcpServers" '(keys - ["$schema","mcpServers"]) == []' mcp.json
check "mcp.json stdio servers use only spec fields and a single-token command" '
  [.mcpServers[] | select(.type == "stdio")
   | ((keys - ["type","command","args","env","cwd"]) == [])
     and (.command | test("^(\\./[^ ]+|[^/ \\\\]+)$"))] | all' mcp.json
check "mcp.json env has no reserved or secret entries" '
  [.mcpServers[] | .env // {} | to_entries[]
   | (.key | IN("PLUGIN_ROOT","PLUGIN_DATA")) or (.value | test("AIza|^AQ\\.|sk-|ghp_"))] | any | not' mcp.json

echo "== Claude Code plugin"
check "userConfig references are declared" '
  (.userConfig // {} | keys) as $declared
  | [.. | strings | scan("\\$\\{user_config\\.([A-Za-z0-9_]+)\\}") | .[0]] | all(IN($declared[]))' .claude-plugin/plugin.json
check "API key option is sensitive" '.userConfig.gemini_api_key.sensitive == true' .claude-plugin/plugin.json
check "no literal secrets in mcpServers env" '[.mcpServers[].env // {} | .[] | test("AIza|^AQ\\.")] | any | not' .claude-plugin/plugin.json

echo "== Gemini CLI extension"
check "name uses letters, digits and dashes" '.name | test("^[a-zA-Z0-9-]+$")' gemini-extension.json
check "settings entries use name/description/envVar/sensitive only" '[.settings[] | (keys - ["name","description","envVar","sensitive"]) == [] and has("envVar")] | all' gemini-extension.json
ctx=$(jq -r '.contextFileName // empty' gemini-extension.json)
if [ -z "$ctx" ] || [ -f "$ctx" ]; then ok "contextFileName ${ctx:-<none>} exists"; else fail "contextFileName $ctx does not exist"; fi

echo "== MCP Registry server.json"
check "\$schema is 2025-12-11" '."$schema" == "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json"' server.json
check "description is at most 100 characters" '.description | length <= 100' server.json
check "OCI package persists /state on a named volume (ledger, budgets, video jobs)" '[.packages[] | select(.registryType == "oci") | .runtimeArguments[] | select(.name == "-v") | .value | test("^[A-Za-z0-9][A-Za-z0-9_.-]*:/state$")] | any' server.json
check "MCPB URL contains \"mcp\" and is a GitHub release asset" '.packages[] | select(.registryType == "mcpb") | .identifier | test("^https://github.com/.+/releases/download/.+mcp")' server.json
if jq -e '.packages[] | select(.registryType == "mcpb") | .fileSha256 | test("^0{64}$")' server.json >/dev/null; then
  if [ "$release" = true ]; then fail "server.json MCPB fileSha256 is still the placeholder"; else ok "MCPB fileSha256 is the placeholder (stamped by the release workflow)"; fi
else
  check "MCPB fileSha256 is a sha256" '.packages[] | select(.registryType == "mcpb") | .fileSha256 | test("^[a-f0-9]{64}$")' server.json
fi

echo "== MCPB manifest"
check "manifest_version is 0.3 with a binary server" '.manifest_version == "0.3" and .server.type == "binary"' mcpb/manifest.json
check "user_config references are declared" '
  (.user_config // {} | keys) as $declared
  | [.server.mcp_config | .. | strings | scan("\\$\\{user_config\\.([A-Za-z0-9_]+)\\}") | .[0]] | all(IN($declared[]))' mcpb/manifest.json
check "optional user_config fields have a default (hosts leave unset ones literal)" '[.user_config[] | select(.required != true) | has("default")] | all' mcpb/manifest.json
check "platform overrides cover win32 and linux" '.server.mcp_config.platform_overrides | has("win32") and has("linux")' mcpb/manifest.json
check "privacy policy declared" '.privacy_policies | length > 0' mcpb/manifest.json

echo "== Layout invariants"
for d in bin hooks agents commands; do
  if [ -e "$d" ]; then fail "top-level $d/ must not exist (claude.ai refuses bin/; other harnesses auto-load the rest)"; fi
done
for f in CLAUDE.md GEMINI.md; do
  if [ -e "$f" ]; then fail "root $f must not exist (it would be injected as project context)"; fi
done
links=$(find . \( -path ./.git -o -path ./dist -o -path ./.build -o -name node_modules \) -prune -o -type l -print)
if [ -n "$links" ]; then fail "symlinks are not allowed: $links"; else ok "no symlinks"; fi
[ -f .mcp.json ] && fail ".mcp.json at the root would add a second server in Claude Code"
skill_count=0
for skill in skills/*/SKILL.md; do
  [ -f "$skill" ] || continue
  skill_count=$((skill_count + 1))
  dir=$(basename "$(dirname "$skill")")
  name=$(frontmatter "$skill" '^name:' | head -1 | sed -E 's/^name:[[:space:]]*//; s/^"//; s/"[[:space:]]*$//')
  if [ "$name" != "$dir" ]; then fail "$skill: name '$name' must equal its directory '$dir'"; fi
  if ! [[ $name =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || [ ${#name} -gt 64 ]; then fail "$skill: invalid skill name '$name'"; fi
  [ -n "$(frontmatter "$skill" '^description:')" ] || fail "$skill: missing description"
done
if [ "$skill_count" -gt 0 ]; then ok "$skill_count skills with name == directory"; else fail "no skills found in skills/"; fi

echo "== External validators"
if have claude; then
  for target in .claude-plugin/plugin.json .claude-plugin/marketplace.json; do
    if out=$(claude plugin validate --strict "$target" 2>&1); then ok "claude plugin validate --strict $target"; else fail "claude plugin validate $target:"; printf '%s\n' "$out" | indent; fi
  done
else
  skip "claude plugin validate (claude CLI not installed)"
fi

if [ -f scripts/validate-skills.py ] && have python3; then
  if out=$(python3 scripts/validate-skills.py skills 2>&1); then ok "scripts/validate-skills.py (Agent Skills spec)"; else fail "scripts/validate-skills.py:"; printf '%s\n' "$out" | indent; fi
else
  skip "scripts/validate-skills.py (python3 or script missing)"
fi

if have goreleaser; then
  if out=$(goreleaser check 2>&1); then ok "goreleaser check"; else fail "goreleaser check:"; printf '%s\n' "$out" | indent; fi
else
  skip "goreleaser check (goreleaser not installed)"
fi

if have shellcheck; then
  if out=$(shellcheck scripts/*.sh mcpb/launch.sh 2>&1); then ok "shellcheck"; else fail "shellcheck:"; printf '%s\n' "$out" | indent; fi
else
  skip "shellcheck (not installed)"
fi

if [ "$offline" = true ]; then
  skip "mcpb validate, mcp-publisher validate (--offline)"
else
  if have npx; then
    stage=$(mktemp -d)
    trap 'rm -rf "$stage"' EXIT
    cp mcpb/manifest.json "$stage/manifest.json"
    entry=$(jq -r .server.entry_point mcpb/manifest.json)
    mkdir -p "$stage/$(dirname "$entry")"
    printf '#!/bin/sh\n' >"$stage/$entry" && chmod +x "$stage/$entry"
    if out=$(npx -y @anthropic-ai/mcpb@2.1.2 validate "$stage" 2>&1); then
      ok "mcpb validate"
    elif echo "$out" | grep -qiE 'ENOTFOUND|ECONNREFUSED|ETIMEDOUT|EAI_AGAIN|network'; then
      skip "mcpb validate (could not download @anthropic-ai/mcpb)"
    else
      fail "mcpb validate:"; printf '%s\n' "$out" | indent
    fi
  else
    skip "mcpb validate (npx not installed)"
  fi
  if have mcp-publisher; then
    if out=$(mcp-publisher validate server.json 2>&1); then ok "mcp-publisher validate"; else fail "mcp-publisher validate:"; printf '%s\n' "$out" | indent; fi
  else
    skip "mcp-publisher validate (mcp-publisher not installed)"
  fi
fi

echo
if [ "$errors" -gt 0 ]; then
  echo "packaging validation FAILED: $errors error(s), $warnings warning(s)"
  exit 1
fi
echo "packaging validation passed ($warnings warning(s))"
