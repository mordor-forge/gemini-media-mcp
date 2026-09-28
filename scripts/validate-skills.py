#!/usr/bin/env python3
"""Validate skills against the Agent Skills specification (agentskills.io).

Checks, per skill directory:
  - SKILL.md exists with YAML frontmatter
  - name: 1-64 chars, lowercase letters/digits/hyphens, no leading/trailing or
    double hyphens, equal to the folder name
  - description: 1-1024 chars
  - only portable frontmatter keys (claude.ai rejects anything else)
  - body under 500 lines
  - relative links in SKILL.md resolve inside the skill
  - evals/*.json files parse
  - no references to tools removed in v1 of the server

Dependency-free so it runs anywhere CI has Python 3.
Usage: scripts/validate-skills.py [skills-dir]
"""
import json
import pathlib
import re
import sys

ALLOWED_KEYS = {"name", "description", "license", "compatibility", "metadata", "allowed-tools"}
NAME_RE = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")
LINK_RE = re.compile(r"\[[^\]]*\]\(([^)#\s]+)(?:#[^)]*)?\)")
REMOVED_TOOLS = ["compose_images", "animate_image", "video_status", "download_video", "generate_audio"]


def parse_frontmatter(text):
    if not text.startswith("---\n"):
        raise ValueError("missing YAML frontmatter")
    end = text.find("\n---", 4)
    if end < 0:
        raise ValueError("unterminated frontmatter")
    block, body = text[4:end], text[end + 4 :]
    data, current = {}, None
    for raw in block.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        if raw.startswith((" ", "\t")):
            if current is None:
                raise ValueError(f"unexpected indentation: {raw!r}")
            key, _, value = raw.strip().partition(":")
            if not isinstance(data[current], dict):
                data[current] = {}
            data[current][key.strip()] = value.strip().strip("\"'")
            continue
        key, sep, value = raw.partition(":")
        if not sep:
            raise ValueError(f"bad frontmatter line: {raw!r}")
        key, value = key.strip(), value.strip()
        current = key
        data[key] = value.strip("\"'") if value else {}
    return data, body


def validate(skill_dir):
    errors = []
    skill_md = skill_dir / "SKILL.md"
    if not skill_md.is_file():
        return [f"{skill_dir}: missing SKILL.md"]
    text = skill_md.read_text(encoding="utf-8")
    try:
        fm, body = parse_frontmatter(text)
    except ValueError as e:
        return [f"{skill_md}: {e}"]

    name = fm.get("name", "")
    if not isinstance(name, str) or not NAME_RE.match(name) or len(name) > 64:
        errors.append(f"{skill_md}: invalid name {name!r}")
    if name != skill_dir.name:
        errors.append(f"{skill_md}: name {name!r} must equal folder {skill_dir.name!r}")
    desc = fm.get("description", "")
    if not isinstance(desc, str) or not (1 <= len(desc) <= 1024):
        errors.append(f"{skill_md}: description must be 1-1024 chars (got {len(desc) if isinstance(desc, str) else 'non-string'})")
    extra = set(fm) - ALLOWED_KEYS
    if extra:
        errors.append(f"{skill_md}: non-portable frontmatter keys {sorted(extra)}")
    if "metadata" in fm and not isinstance(fm["metadata"], dict):
        errors.append(f"{skill_md}: metadata must be a mapping")

    lines = text.count("\n") + 1
    if lines >= 500:
        errors.append(f"{skill_md}: {lines} lines; keep SKILL.md under 500 (move detail to references/)")

    for target in LINK_RE.findall(body):
        if re.match(r"^[a-z]+:", target):
            continue  # URLs
        resolved = (skill_dir / target).resolve()
        if not resolved.exists():
            errors.append(f"{skill_md}: broken link {target}")
        elif skill_dir.resolve() not in resolved.parents and resolved != skill_dir.resolve():
            errors.append(f"{skill_md}: link {target} leaves the skill directory")

    for md in skill_dir.rglob("*.md"):
        content = md.read_text(encoding="utf-8")
        for tool in REMOVED_TOOLS:
            for m in re.finditer(rf"\b{tool}\b", content):
                line = content[: m.start()].count("\n")
                context = content.splitlines()[line].lower()
                if "migrat" not in context and "v0" not in context and "renamed" not in context and "replaced" not in context:
                    errors.append(f"{md}: references removed tool {tool} (line {line + 1})")

    for js in (skill_dir / "evals").glob("*.json") if (skill_dir / "evals").is_dir() else []:
        try:
            json.loads(js.read_text(encoding="utf-8"))
        except json.JSONDecodeError as e:
            errors.append(f"{js}: invalid JSON: {e}")
    return errors


def main():
    root = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "skills")
    skills = sorted(p for p in root.iterdir() if p.is_dir())
    if not skills:
        print(f"no skills found in {root}")
        return 1
    errors = []
    for s in skills:
        errs = validate(s)
        status = "ok" if not errs else "FAIL"
        print(f"{status:4} {s.name}")
        errors += errs
    for e in errors:
        print("  -", e)
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
