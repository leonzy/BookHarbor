"""Exercise generated remote programs and failure rollback without a live NAS."""
import ast
import importlib.util
import json
import os
import subprocess
import sys
from pathlib import Path

import pytest


def load_deployer():
    path = Path(__file__).resolve().parents[1] / "scripts/deploy_metadata_enrichment.py"
    spec = importlib.util.spec_from_file_location("deploy_metadata_enrichment", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@pytest.mark.parametrize("fail_nginx,use_frontend", [(False, False), (True, False), (False, True)])
def test_metadata_deployment_preserves_main_and_rolls_back_on_nginx_failure(tmp_path, monkeypatch, fail_nginx, use_frontend):
    deploy = load_deployer()
    binary = tmp_path / "booksvr"
    binary.write_bytes(b"\x7fELFfixture")
    monkeypatch.setattr(sys, "argv", ["deploy", "--host", "fixture", "--binary", str(binary), "--revision", "fixture-sha"])
    if use_frontend:
        frontend = tmp_path / "public"
        (frontend / "_nuxt").mkdir(parents=True)
        (frontend / "_i18n").mkdir()
        (frontend / "_i18n/hash/zh-CN").mkdir(parents=True)
        (frontend / "_i18n/hash/zh-CN/messages.json").write_text('{"zh-CN":{"common":{"cancel":"Cancel"}}}')
        (frontend / "200.html").write_text('<html><body>fixture SPA</body></html>')
        (frontend / "_nuxt/chunk.js").write_text('fixture')
        sys.argv.extend(["--frontend", str(frontend)])
    programs = []
    state = {"talebook:booksvr": {"pid": 70, "start": "123"},
             "bookharbor-native-reader": {"pid": 3934, "start": "456"}}

    def ssh(command, input=None):
        import shlex
        remote = shlex.split(command[2])
        if "python3" not in remote:
            assert remote[-1] in ["reread", "bookharbor-metadata"]
            return b"ok"
        code = remote[-1]
        ast.parse(code)  # Validate actual generated programs, including escaped SQL/config.
        programs.append(code)
        if "for name in ['talebook:booksvr'" in code:
            return json.dumps(state).encode()
        if fail_nginx and "Only metadata API locations switched" in code:
            raise subprocess.CalledProcessError(1, command, stderr=b"nginx configuration rejected")
        if "Only metadata API locations switched" in code:
            from types import SimpleNamespace
            site = tmp_path / "nginx.conf"
            site.write_text('    location ~ ^/(api|get|read|read-comic|opds|auth|books|media)/ {\n    location ^~ /_nuxt/ {\n')
            site.chmod(0o600)
            adapted = code.replace('/etc/nginx/conf.d/talebook.conf', site.as_posix())
            adapted = adapted.replace('import subprocess,socket,time', '')
            class ConnectedSocket:
                def __enter__(self): return self
                def __exit__(self, *args): pass
            exec(adapted, {
                'subprocess': SimpleNamespace(run=lambda *args, **kwargs: SimpleNamespace(stdout='RUNNING')),
                'socket': SimpleNamespace(create_connection=lambda *args, **kwargs: ConnectedSocket()),
            })
            assert 'book/[0-9]+(?:/(?:edit|refer))?' in site.read_text()
            assert 'location = /get/pcover {' in site.read_text()
            assert 'expires off;' in site.read_text()
            if os.name == 'posix':
                assert site.stat().st_mode & 0o004
        if use_frontend and "SPA entries and locale assets updated" in code:
            live = tmp_path / "live"
            live.mkdir()
            (live / "index.html").write_text('<html data-talebook-version="v-before">old</html>')
            (live / "_nuxt").mkdir()
            (live / "_nuxt/previous.js").write_text('previous asset')
            maintenance = tmp_path / "maintenance"
            maintenance.mkdir(mode=0o700)
            adapted = code.replace('/data/maintenance/20261030-file-metadata-enrichment', maintenance.as_posix())
            adapted = adapted.replace('/run/talebook-release/current/app/dist', live.as_posix())
            result = subprocess.run([sys.executable, '-c', 'import os;os.umask(0o077)\n' + adapted], input=input,
                                    capture_output=True, check=True)
            assert b'SPA entries and locale assets updated' in result.stdout
            assert 'data-talebook-version="v-before"' in (live / 'index.html').read_text()
            assert (live / '_nuxt/previous.js').read_text() == 'previous asset'
            assert json.loads((live / '_i18n/hash/zh-CN/messages.json').read_text())['zh-CN']
            if os.name == 'posix':
                for directory in [live / '_i18n', live / '_i18n/hash', live / '_i18n/hash/zh-CN']:
                    assert directory.stat().st_mode & 0o005 == 0o005
        return b"ok"

    monkeypatch.setattr(deploy.subprocess, "check_output", ssh)
    if fail_nginx:
        with pytest.raises(subprocess.CalledProcessError):
            deploy.main()
        assert any("shutil.copy2(base/'nginx-before.conf'" in code for code in programs)
    else:
        deploy.main()
        assert any("preserved_processes" in code and "fixture-sha" in code for code in programs)
    mutations = "\n".join(code for code in programs if "for name in ['talebook:booksvr'" not in code)
    assert "stop','bookharbor-metadata'" in mutations
    assert "stop','talebook:booksvr'" not in mutations
    assert "restart','talebook:booksvr'" not in mutations
    assert "GOMAXPROCS" in mutations and "128MiB" in mutations
    assert "'status','bookharbor-metadata'" in mutations
    assert "'start','bookharbor-metadata'" in mutations
    assert "if 'RUNNING' not in status.stdout" in mutations
    if use_frontend:
        assert "data-talebook-version" in mutations
        assert "Readest and old hashed assets retained" in mutations
