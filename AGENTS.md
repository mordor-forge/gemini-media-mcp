# AGENTS.md

Guide for coding agents working on **gemini-media-mcp**: a Go MCP server
(module `github.com/mordor-forge/gemini-media-mcp`, binary `gemini-media-mcp`,
MCP server name `gemini-media`) for Google's generative media models - Nano
Banana images, Veo video, Gemini TTS and Lyria music - on the Gemini API or
Vertex AI. Go 1.26+ (see `go.mod`).

## Commands

```sh
go build ./...                 # build
go test -race ./...            # unit tests (no network, no API key)
go vet ./...
golangci-lint run              # lint (golangci-lint v2)
go test ./internal/catalog     # after editing internal/catalog/models.yaml

go run ./cmd/gemini-media-mcp doctor          # check credentials/models (makes a free list call)
go run ./cmd/gemini-media-mcp models --all    # print the catalog
npx @modelcontextprotocol/inspector go run ./cmd/gemini-media-mcp   # poke the tools interactively

scripts/validate-packaging.sh  # after touching any manifest, script or skill
node --test npm/test/*.test.js # npm launcher tests
```

Run build, vet, tests and lint before you say a change is done.

## Rules that are easy to break

- **E2E tests cost real money.** `internal/media/e2e_test.go` (build tag
  `e2e`) calls Google's paid APIs and needs `GEMINI_MEDIA_E2E=1` plus
  `GEMINI_API_KEY` (or Vertex credentials); the video test alone costs about
  $0.20. Never run them - or any command that generates media - unless the
  user explicitly asks. `go test ./...` without the tag is free.
- **stdout is the MCP JSON-RPC channel.** In `serve` (stdio) mode any byte
  written to stdout that is not protocol traffic breaks the client. Log with the
  injected `*slog.Logger` (it writes to stderr); never `fmt.Print*` or
  `log.Print*` to stdout from server code. The same holds for
  `npm/gemini-media-mcp/bin/gemini-media-mcp.js` and `mcpb/launch.sh`. Only
  the one-shot subcommands (`doctor`, `models`, `usage`, `version`,
  `configure`) print to stdout.
- **Never log, echo or return credentials.** Config files holding keys are
  written with mode 0600; keep it that way.
- **Model facts live in data, not code.** `internal/catalog/models.yaml` is the
  single source of truth for model IDs, aliases, lifecycle dates,
  capabilities, constraints and prices. When Google adds, renames, reprices or
  retires a model, edit the YAML - not Go code.
- **Errors must be actionable.** Return `internal/apperr` classified errors
  whose message tells the agent or user what to do next (which parameter,
  which command, which setting).

## Architecture

```
cmd/gemini-media-mcp   CLI + composition root: serve | doctor | configure | models | usage | version
internal/server        MCP tools/resources over stdio or Streamable HTTP (auth token, origin checks)
internal/media         generation workflows behind the tools; cost estimates, budget checks
internal/google        adapter over google.golang.org/genai (Gemini API / Vertex), retries, error mapping
internal/catalog       models.yaml (embedded) + optional hot-reloaded override file; alias resolution, pricing
internal/config        layered config: defaults < config.yaml < env (GEMINI_MEDIA_*, GOOGLE_*) < flags; auth selection
internal/store         saving generated files (collision-free names), input-file access policy
internal/spend         append-only usage ledger (usage.jsonl) and session/daily/monthly budgets
internal/filelock      cross-process advisory file lock (flock / LockFileEx) guarding the ledger
internal/jobs          persisted long-running jobs (Veo video operations)
internal/apperr        classified, agent-actionable errors
internal/version       Version/Commit/Date injected with -ldflags (see .goreleaser.yaml, Dockerfile)
```

A tool call flows `server` -> `media.Service` -> (`catalog` resolves the
model and price, `spend` checks the budget) -> `google.Pool` -> `store`
saves the output -> `spend` records the cost.

## Adding or updating a model

1. Edit `internal/catalog/models.yaml`: `id`, `aliases`, status and
   shutdown dates, capabilities/constraints, prices, and bump the top-level
   `version:`; add the doc URL to `sources` if new.
2. `go test ./internal/catalog` (then `go test ./...`).
3. `go run ./cmd/gemini-media-mcp models --all` and check the row.
4. Update model tables in `skills/*/SKILL.md` or `references/` if they name
   the model.
5. Do not run live generation to "verify" unless asked (see E2E above).

## Packaging invariants

The repository root is the plugin root for every harness:

| File | Consumer |
|---|---|
| `plugin.json`, `mcp.json` | Agent Plugins 1.0 (Codex, VS Code/Copilot, Cursor) |
| `.codex-plugin/plugin.json` | Codex overlay: env vars forwarded to the server |
| `.claude-plugin/plugin.json`, `.claude-plugin/marketplace.json` | Claude Code plugin + marketplace (also read by Codex, VS Code, `npx skills`) |
| `gemini-extension.json`, `packaging/gemini/GEMINI-EXTENSION.md` | Gemini CLI extension |
| `server.json` | MCP Registry |
| `mcpb/` | Claude Desktop bundle (`.mcpb`) |
| `npm/` | `npx gemini-media-mcp` launcher + per-platform binary packages |
| `Dockerfile` | `ghcr.io/mordor-forge/gemini-media-mcp` |
| `skills/` | Agent Skills, discovered by every harness above |

- `skills/` is the **only** copy of the skills: no symlinks, no duplicates in
  `.claude/`, `.agents/` or elsewhere. Each skill's `name` equals its
  directory name.
- No top-level `bin/`, `hooks/`, `agents/` or `commands/` directories, no
  root `CLAUDE.md`, `GEMINI.md` or `.mcp.json` (harnesses would auto-load them).
- No secrets in any manifest. Credentials come from the harness's secret
  storage, the environment, or `gemini-media-mcp configure --api-key-stdin`.
  Plugin-injected values use `GEMINI_MEDIA_*` names; empty values are ignored
  by the server, so optional fields may be left blank.
- Names: MCP server key `gemini-media` everywhere; plugin `gemini-media`;
  Gemini extension `gemini-media-mcp` (= its install directory and the release
  archive suffix); registry name `io.github.mordor-forge/gemini-media-mcp`
  (must match `mcpName` in the npm package and the Docker label).
- **Versions:** never edit version strings by hand. Run
  `scripts/sync-version.sh X.Y.Z` (stamps every manifest, the pinned
  `gemini-media-mcp@X.Y.Z` npx arguments, npm dependencies, `server.json`
  and the skills' `metadata.version`), then `scripts/validate-packaging.sh`,
  commit, and push tag `vX.Y.Z`. The release workflow rejects a tag whose
  manifests disagree.
- A new user-facing setting or env var usually needs: `internal/config`,
  the README, `.claude-plugin/plugin.json` `userConfig`,
  `gemini-extension.json` `settings`, `mcpb/manifest.json` `user_config`
  (optional fields need `"default": ""`), `server.json`
  `environmentVariables` and `.codex-plugin/plugin.json` `env_vars`.
- A new or renamed tool: also update the `tools` list in
  `mcpb/manifest.json` (release builds regenerate it from the binary) and the
  skills that mention it.

## Pull requests

- Conventional commit subjects (`feat:`, `fix:`, `catalog:`, `docs:`,
  `test:`, `ci:`, `chore:`); the release changelog is grouped by them.
- One focused change per PR, with tests for behavior changes. Update the README
  and `docs/` when flags, env vars, tools or defaults change.
- Never commit `dist/`, `.build/`, `*.mcpb`, built binaries or credentials.
- Releases are cut only by maintainers pushing a `v*` tag
  (`.github/workflows/release.yml`); do not tag or publish from an agent
  session unless asked.
