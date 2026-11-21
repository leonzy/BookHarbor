#!/usr/bin/env python3
"""Validate locale JSON/compiler safety and report translation coverage."""

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
APP_DIR = ROOT / "app"
LOCALES_DIR = APP_DIR / "i18n" / "locales"
DEFAULT_LOCALE = "zh-CN"
EXCLUDED_DIRS = {"node_modules", ".nuxt", ".output", ".git", "coverage", "dist", "public", "test"}
KEY_PATTERN = re.compile(r"(?<![\w$])(?:\$t|t)\(\s*(['\"`])([a-zA-Z0-9_.]+)\1")
KEYPATH_PATTERN = re.compile(r"keypath\s*=\s*(['\"])([a-zA-Z0-9_.]+)\1")


def flatten(value, prefix=""):
    out = {}
    if isinstance(value, dict):
        for key, item in value.items():
            path = f"{prefix}.{key}" if prefix else key
            out.update(flatten(item, path))
    else:
        out[prefix] = value
    return out


def load_locales():
    locales = {}
    for path in sorted(LOCALES_DIR.glob("*.json")):
        with path.open(encoding="utf-8") as handle:
            locales[path.stem] = flatten(json.load(handle))
    return locales


def collect_code_keys():
    keys = set()
    for path in APP_DIR.rglob("*"):
        if not path.is_file() or path.suffix not in {".vue", ".js", ".ts"}:
            continue
        if any(part in EXCLUDED_DIRS for part in path.relative_to(APP_DIR).parts):
            continue
        text = path.read_text(encoding="utf-8")
        for pattern in (KEY_PATTERN, KEYPATH_PATTERN):
            keys.update(match.group(2) for match in pattern.finditer(text))
    return keys


def validate(locales, code_keys):
    problems = []
    if DEFAULT_LOCALE not in locales:
        return [f"missing default locale: {DEFAULT_LOCALE}.json"]

    for locale, values in sorted(locales.items()):
        for key, value in values.items():
            if isinstance(value, str):
                unescaped = value.replace("{'@'}", "")
                if "@" in unescaped or "<" in unescaped:
                    problems.append(f"{locale}: {key} contains a raw @ or < character")
    return problems


def main():
    locales = load_locales()
    code_keys = collect_code_keys()
    problems = validate(locales, code_keys)
    if problems:
        for problem in problems:
            print(problem, file=sys.stderr)
        print(f"i18n check failed with {len(problems)} problem(s)", file=sys.stderr)
        return 1
    default_keys = set(locales[DEFAULT_LOCALE])
    missing_references = sorted(code_keys - default_keys)
    if missing_references:
        print(f"Review: {len(missing_references)} static code reference(s) have no default key; dynamic keys may be included")
    for locale, values in sorted(locales.items()):
        fallback_count = len(default_keys - set(values))
        if fallback_count:
            print(f"Review: {locale} uses fallback for {fallback_count} default key(s)")
    print(
        f"i18n JSON/compiler safety check passed: {len(locales)} locales, "
        f"{len(default_keys)} default keys, {len(code_keys)} static references"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
