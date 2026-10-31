"""The history hotfix routes only read-only history endpoints to the sidecar."""
import importlib.util
from pathlib import Path
import subprocess

import pytest


def deployer():
    path = Path(__file__).resolve().parents[1] / "scripts/deploy_plugin_run_history.py"
    spec = importlib.util.spec_from_file_location("deploy_plugin_run_history", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_history_proxy_scope_and_idempotence(tmp_path):
    deploy = deployer()
    original = "    location ~ ^/(api|get|read|read-comic|opds|auth|books|media)/ {\n    }\n"
    site, backup = tmp_path / "site.conf", tmp_path / "backup.conf"
    site.write_text(original)
    calls = []
    deploy.configure_run_history_proxy(site, backup, run=lambda cmd, **kw: calls.append(cmd))
    configured = site.read_text()
    assert "admin/plugins/runs(?:/[0-9]+)?" in configured
    assert "plugins/runs(?:/[0-9]+)?" in configured
    assert configured.index("admin/plugins/runs") < configured.index("(api|get|")
    assert "/retry" not in configured and "/rollback" not in configured
    assert backup.read_text() == original
    deploy.configure_run_history_proxy(site, backup, run=lambda *a, **kw: pytest.fail("reloaded twice"))
    assert site.read_text() == configured
    assert calls == [["nginx", "-t"], ["nginx", "-s", "reload"]]


@pytest.mark.parametrize("failure", ["-t", "-s"])
def test_history_proxy_rollback(tmp_path, failure):
    deploy = deployer()
    site, backup = tmp_path / "site.conf", tmp_path / "backup.conf"
    original = "    location ~ ^/(api|get|read|read-comic|opds|auth|books|media)/ {\n    }\n"
    site.write_text(original)

    def fail(command, **kwargs):
        if command[1] == failure:
            raise subprocess.CalledProcessError(1, command)

    with pytest.raises(subprocess.CalledProcessError):
        deploy.configure_run_history_proxy(site, backup, run=fail)
    assert site.read_text() == original
