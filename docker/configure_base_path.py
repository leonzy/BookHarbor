"""Bake deployment redirects into the image; requests arrive without the prefix."""

import sys
from pathlib import Path

import os
import re

def normalize_base_path(value):
    if value in ("", "/"):
        return ""
    if not isinstance(value, str) or not re.fullmatch(r"(?:/[A-Za-z0-9_-]+)+/?", value):
        raise ValueError("TALEBOOK_BASE_PATH must be empty or /path[/path] using letters, digits, _ and -")
    return value.rstrip("/")

BASE_PATH = normalize_base_path(os.environ.get("TALEBOOK_BASE_PATH", ""))


def configure(text, prefix=BASE_PATH):

    prefix = normalize_base_path(prefix)
    if not prefix:
        return text
    if "absolute_redirect off;" not in text:
        text = text.replace("    server_name _;", "    server_name _;\n    absolute_redirect off;")
    for path in ("/opds/", "/books/"):
        text = text.replace("return 302 " + path + ";", "return 302 " + prefix + path + ";")
    # Nuxt's server accepts the public base URL, unlike Tornado's internal routes.
    for upstream in ("nuxtjs", "nuxtdev"):
        marker = "        proxy_pass       http://" + upstream + ";"
        text = text.replace(marker, "        rewrite ^ " + prefix + "$uri break;\n" + marker)
    return text


if __name__ == "__main__":
    path = Path(sys.argv[1])
    path.write_text(configure(path.read_text()))
