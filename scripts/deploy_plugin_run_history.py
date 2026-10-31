"""Deploy registered plugin-run-history routes without restarting the importer."""
import argparse
import inspect
from pathlib import Path
import shlex
import subprocess
import sys


def run_history_proxy_config(text):
    marker = "    location ~ ^/(api|get|read|read-comic|opds|auth|books|media)/ {"
    block = """    # Plugin run history shares maintained read-only access checks.
    location ~ ^/(?:api/(?:admin/plugins/runs(?:/[0-9]+)?|plugins/runs(?:/[0-9]+)?))$ {
        expires -1s;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;
        proxy_read_timeout 90s;
        proxy_pass http://127.0.0.1:8004;
    }

"""
    if block in text:
        return text
    if text.count(marker) != 1:
        raise ValueError("Expected one standard API proxy block")
    if "# Plugin run history shares maintained read-only access checks." in text:
        raise ValueError("Unexpected existing run history proxy configuration")
    return text.replace(marker, block + marker)


def configure_run_history_proxy(site, backup, run=subprocess.run):
    old = site.read_bytes()
    new = run_history_proxy_config(old.decode()).encode()
    if new == old:
        return
    backup.parent.mkdir(parents=True, exist_ok=True)
    backup.write_bytes(old)
    backup.chmod(0o600)
    try:
        site.write_bytes(new)
        site.chmod(0o644)
        run(["nginx", "-t"], check=True)
        run(["nginx", "-s", "reload"], check=True)
    except Exception:
        site.write_bytes(old)
        site.chmod(0o644)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", required=True)
    parser.add_argument("--container", default="bookharbor")
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--frontend", type=Path)
    args = parser.parse_args()
    command = [sys.executable, str(Path(__file__).with_name("deploy_metadata_enrichment.py")),
               "--host", args.host, "--container", args.container,
               "--binary", str(args.binary), "--revision", args.revision]
    if args.frontend:
        command += ["--frontend", str(args.frontend)]
    subprocess.run(command, check=True)
    code = "from pathlib import Path\nimport subprocess\n" + inspect.getsource(run_history_proxy_config)
    code += "\n" + inspect.getsource(configure_run_history_proxy)
    code += (
        "\nconfigure_run_history_proxy(Path('/etc/nginx/conf.d/talebook.conf'),"
        "Path('/data/maintenance/20261031-plugin-run-history/nginx-before.conf'))\n"
    )
    subprocess.run(["ssh", args.host, shlex.join(["docker", "exec", "-i", "-u", "0", args.container, "python3", "-"])],
                   input=code, text=True, check=True)
    # This changes only restart policy; it does not stop or recreate the container.
    subprocess.run(
        ["ssh", args.host, shlex.join(["docker", "update", "--restart", "unless-stopped", args.container])], check=True
    )


if __name__ == "__main__":
    main()
