"""Image-owned support has no dependency on the retired HTTP application."""
from pathlib import Path

import pytest

from docker.configure_base_path import configure, normalize_base_path
from docker import self_check


@pytest.mark.parametrize("value", ["/a\nb", "/a;return 200", "/../a", " /a", "http://host", None])
def test_base_path_rejects_config_injection(value):
    with pytest.raises(ValueError):
        normalize_base_path(value)


def test_public_redirect_prefix_is_preserved():
    assert "return 302 /shelf/opds/;" in configure("return 302 /opds/;", "/shelf/")


def test_cookie_secret_is_read_without_importing_application(tmp_path, monkeypatch):
    settings = tmp_path / "books/settings"
    settings.mkdir(parents=True)
    (settings / "auto.py").write_text("settings = {'cookie_secret': 'native-secret'}", encoding="utf8")
    monkeypatch.setattr(self_check, "DATA_DIR", str(tmp_path))
    assert self_check.read_cookie_secret() == "native-secret"


def test_no_python_http_application_in_production_recipe():
    root = Path(__file__).resolve().parents[1]
    recipe = (root / "Dockerfile").read_text(encoding="utf8")
    assert "COPY webserver/" not in recipe
    assert "python3 server.py" not in recipe
    assert "booksvr bootstrap" in recipe


def test_manual_secret_overrides_auto_without_executing_code(tmp_path, monkeypatch):
    settings = tmp_path / "books/settings"
    settings.mkdir(parents=True)
    (settings / "auto.py").write_text("settings = {'cookie_secret': 'auto-secret'}", encoding="utf8")
    (settings / "manual.py").write_text("settings = {'cookie_secret': 'manual-secret'}", encoding="utf8")
    monkeypatch.setattr(self_check, "DATA_DIR", str(tmp_path))
    assert self_check.read_cookie_secret() == "manual-secret"
