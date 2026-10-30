"""Deploy registered network-library routes without restarting the importer."""
import argparse
import inspect
from pathlib import Path
import shlex
import subprocess
import sys


def network_proxy_config(text):
    marker = "    location ~ ^/(api|get|read|read-comic|opds|auth|books|media)/ {"
    block = """    # Network sources share the maintained provider registry.
    location ~ ^/(?:api/(?:book-sources(?:/.*)?|network/.*)|read-online-comic)$ {
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
    if "# Network sources share the maintained provider registry." in text:
        raise ValueError("Unexpected existing network proxy configuration")
    return text.replace(marker, block + marker)


def configure_network_proxy(site, backup, run=subprocess.run):
    old = site.read_bytes()
    new = network_proxy_config(old.decode()).encode()
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
    args = parser.parse_args()
    subprocess.run([sys.executable, str(Path(__file__).with_name("deploy_metadata_enrichment.py")),
                    "--host", args.host, "--container", args.container,
                    "--binary", str(args.binary), "--revision", args.revision], check=True)
    code = "from pathlib import Path\nimport subprocess\n" + inspect.getsource(network_proxy_config)
    code += "\n" + inspect.getsource(configure_network_proxy)
    code += (
        "\nconfigure_network_proxy(Path('/etc/nginx/conf.d/talebook.conf'),"
        "Path('/data/maintenance/20261031-network-library/nginx-before.conf'))\n"
    )
    subprocess.run(["ssh", args.host, shlex.join(["docker", "exec", "-i", "-u", "0", args.container, "python3", "-"])],
                   input=code, text=True, check=True)
    # This changes only restart policy; it does not stop or recreate the container.
    subprocess.run(
        ["ssh", args.host, shlex.join(["docker", "update", "--restart", "unless-stopped", args.container])], check=True
    )


if __name__ == "__main__":
    main()
