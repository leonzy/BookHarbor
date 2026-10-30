"""Verify maintenance routing precedence, repeat deployment, and rollback."""
import importlib.util
from pathlib import Path
import subprocess

import pytest


def deployer():
    path = Path(__file__).resolve().parents[1] / "scripts/deploy_network_library.py"
    spec = importlib.util.spec_from_file_location("deploy_network_library", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_network_routes_precede_generic_api_and_preserve_other_services(tmp_path):
    deploy = deployer()
    original = """server {
    location ^~ /opds/ { proxy_pass http://127.0.0.1:8004; }
    location ~ ^/(api|get|read|read-comic|opds|auth|books|media)/ {
        proxy_pass http://127.0.0.1:8000;
    }
}
"""
    site, backup = tmp_path / "site.conf", tmp_path / "backup.conf"
    site.write_text(original)
    calls = []
    deploy.configure_network_proxy(site, backup, run=lambda command, **kw: calls.append(command))
    result = site.read_text()
    assert result.index("api/(?:book-sources") < result.index("(api|get|read|")
    assert "network/.*" in result and "read-online-comic" in result
    assert result.count("proxy_pass http://127.0.0.1:8000;") == 1
    assert "location ^~ /opds/ { proxy_pass http://127.0.0.1:8004; }" in result
    assert backup.read_text() == original
    assert calls == [["nginx", "-t"], ["nginx", "-s", "reload"]]
    deploy.configure_network_proxy(site, backup, run=lambda *a, **kw: pytest.fail("idempotent deploy reloaded"))
    assert site.read_text() == result and backup.read_text() == original


@pytest.mark.parametrize("failure", ["-t", "-s"])
def test_network_proxy_restores_config_on_failure(tmp_path, failure):
    deploy = deployer()
    site, backup = tmp_path / "site.conf", tmp_path / "backup.conf"
    original = "    location ~ ^/(api|get|read|read-comic|opds|auth|books|media)/ {\n    }\n"
    site.write_text(original)

    def run(command, **kw):
        if command[1] == failure:
            raise subprocess.CalledProcessError(1, command)

    with pytest.raises(subprocess.CalledProcessError):
        deploy.configure_network_proxy(site, backup, run=run)
    assert site.read_text() == original


def test_network_proxy_rejects_unknown_layout():
    with pytest.raises(ValueError):
        deployer().network_proxy_config("server { }")


def test_deployment_changes_restart_policy_without_restarting_container(monkeypatch, tmp_path):
    import sys
    deploy = deployer()
    calls = []

    def run(command, **kwargs):
        calls.append(command)
        if "input" in kwargs:
            compile(kwargs["input"], "generated-nginx-update", "exec")

    monkeypatch.setattr(deploy.subprocess, "run", run)
    monkeypatch.setattr(sys, "argv", ["deploy", "--host", "fixture", "--container", "bookharbor",
                                     "--binary", str(tmp_path / "booksvr"), "--revision", "fixture"])
    deploy.main()
    assert len(calls) == 3
    assert "deploy_metadata_enrichment.py" in calls[0][1]
    assert calls[-1][-1] == "docker update --restart unless-stopped bookharbor"
    assert not any("docker restart" in str(command) or "docker stop" in str(command) for command in calls)
