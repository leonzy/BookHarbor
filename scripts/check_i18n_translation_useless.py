#!/usr/bin/env python3
"""Report translation keys without static code references.

Dynamic keys are common in the application, so this report is informational. The
blocking locale and missing-reference checks live in check_i18n_translation_missing.py.
"""

import argparse

from check_i18n_translation_missing import collect_code_keys, load_locales


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--verbose", "-v", action="store_true", help="List candidate keys")
    args = parser.parse_args()

    code_keys = collect_code_keys()
    locales = load_locales()
    for locale, values in sorted(locales.items()):
        candidates = sorted(set(values) - code_keys)
        print(f"{locale}: {len(candidates)} key(s) have no static t() reference (dynamic use may still apply)")
        if args.verbose:
            for key in candidates:
                print(f"  {key}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
