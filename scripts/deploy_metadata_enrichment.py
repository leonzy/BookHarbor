"""Deploy the bounded metadata listener without restarting imports or readers.

Requires an already configured BookHarbor container, SSH access, and a tested
Linux/amd64 booksvr executable. Authentication stays in the existing entrypoint.
"""
import argparse
import hashlib
import gzip
import io
import json
import shlex
import subprocess
import tarfile
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", required=True)
    parser.add_argument("--container", default="bookharbor")
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--frontend", type=Path, help="Tested Nuxt .output/public directory; replace SPA entries and hashed assets only")
    args = parser.parse_args()
    data = args.binary.read_bytes()
    if data[:4] != b"\x7fELF":
        raise SystemExit("Expected a tested Linux ELF executable")
    digest = hashlib.sha256(data).hexdigest()
    runtime = f"/data/books/.bookharbor-runtime/metadata-{digest[:12]}"
    target = runtime + "/booksvr"
    maintenance = "/data/maintenance/20261030-file-metadata-enrichment"

    def remote(command, body=None):
        return subprocess.check_output(["ssh", args.host, shlex.join(command)], input=body).decode()

    def inside(code, body=None):
        return remote(["docker", "exec", "-i", "-u", "0", args.container, "python3", "-c", code], body)

    state_code = """import json,subprocess
from pathlib import Path
out={}
for name in ['talebook:booksvr','bookharbor-native-reader']:
 pid=int(subprocess.check_output(['supervisorctl','pid',name]));assert pid>0
 out[name]={'pid':pid,'start':Path(f'/proc/{pid}/stat').read_text().split(')')[1].split()[19]}
print(json.dumps(out))
"""
    before = json.loads(inside(state_code))
    print("Preserving", json.dumps(before), flush=True)
    code = """import sys,hashlib,os,json,subprocess,gzip
from pathlib import Path
base=Path(BASE);base.mkdir(mode=0o700,exist_ok=True)
site=Path('/etc/nginx/conf.d/talebook.conf');backup=base/'nginx-before.conf'
if not (base/'nginx-original.conf').exists():(base/'nginx-original.conf').write_bytes(backup.read_bytes() if backup.exists() else site.read_bytes())
backup.write_bytes(site.read_bytes())
program=Path('/etc/supervisor/conf.d/bookharbor-metadata.conf')
if program.exists():
 (base/'supervisor-before.conf').write_bytes(program.read_bytes())
 subprocess.run(['supervisorctl','stop','bookharbor-metadata'],check=True,stdout=subprocess.PIPE)
runtime=Path(RUNTIME);runtime.mkdir(mode=0o700,parents=True,exist_ok=True);os.chown(runtime,1000,1000)
binary=runtime/'booksvr';body=gzip.decompress(sys.stdin.buffer.read());assert hashlib.sha256(body).hexdigest()==DIGEST
if binary.exists():assert binary.read_bytes()==body
else:binary.write_bytes(body);binary.chmod(0o755);os.chown(binary,1000,1000)
print('ELF verified and previous metadata configuration backed up')
""".replace("BASE", repr(maintenance)).replace("RUNTIME", repr(runtime)).replace("DIGEST", repr(digest))
    print(inside(code, gzip.compress(data, compresslevel=3)), flush=True)
    # The deployment lease keeps the worker idle until edit protection is routed.
    freeze = "deployment:" + digest[:12]
    setup = """import sqlite3,time,os
from pathlib import Path
Path('/etc/supervisor/conf.d/bookharbor-metadata.conf').write_text(CONFIG)
os.setgroups([]);os.setgid(1000);os.setuid(1000)
c=sqlite3.connect('/data/books/library/metadata.db',timeout=1)
c.execute('CREATE TABLE IF NOT EXISTS metadata_enrichment_lease(id INTEGER PRIMARY KEY,owner TEXT NOT NULL,expires INTEGER NOT NULL)')
now=int(time.time());r=c.execute("INSERT INTO metadata_enrichment_lease(id,owner,expires) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET owner=excluded.owner,expires=excluded.expires WHERE metadata_enrichment_lease.expires<? OR metadata_enrichment_lease.owner LIKE 'deployment:%' OR metadata_enrichment_lease.owner='maintenance:trusted-metadata-20261030'",(FREEZE,now+120,now));assert r.rowcount==1,'Another metadata worker owns the lease';c.commit();c.close()
print('Only metadata supervisor configuration installed')
"""
    config = f"""[program:bookharbor-metadata]
user=talebook
command=/usr/bin/ionice -c 3 /usr/bin/nice -n 19 /usr/bin/python3 /opt/talebook-upgrade/booksvr-entrypoint.py {target} serve --metadata-only -db=/data/books/library/metadata.db -user-db=/data/books/calibre-webserver.db -addr=127.0.0.1:8004 -tornado-url=http://127.0.0.1:8000 -library-root=/data/books/library -settings-file=/data/books/settings/auto.py -version=v26.10.30-metadata
directory=/run/talebook-release/current
environment=TALEBOOK_SERVER_DIR="/run/talebook-release/current",GOMAXPROCS="1",GOMEMLIMIT="128MiB"
autostart=true
autorestart=true
stopasgroup=true
killasgroup=true
startsecs=3
stopwaitsecs=40
redirect_stderr=true
stdout_logfile={maintenance}/metadata.log
stdout_logfile_maxbytes=1048576
stdout_logfile_backups=2
"""
    setup = setup.replace("FREEZE", repr(freeze)).replace("CONFIG", repr(config))
    try:
        print(inside(setup), flush=True)
        for command in [["supervisorctl", "reread"], ["supervisorctl", "update", "bookharbor-metadata"]]:
            print(remote(["docker", "exec", args.container, *command]), flush=True)
        route = """
    # File-first metadata and protected edits, independent from running imports.
    location ~ ^/api/(?:book/[0-9]+(?:/(?:edit|refer))?|admin/plugins/[a-z0-9.-]+/metadata/search|admin/metadata/enrichment(?:/sources/[0-9]+|/review)?)$ {
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
        opds_route = """    # Bounded OPDS queries beside the uninterrupted importer.
    location ^~ /opds/ {
        proxy_set_header Host $http_host;
        proxy_pass http://127.0.0.1:8004;
    }

"""
        opds_root_route = """    location = /opds {
        return 302 /opds/;
    }

"""
        cover_route = """    location = /get/pcover {
        expires off;
        proxy_read_timeout 45s;
        proxy_pass http://127.0.0.1:8004;
        proxy_set_header Host $http_host;
    }

"""
        locale_guard = """
    # Locale payload failures must remain JSON/404, never the SPA shell.
    location ^~ /_i18n/ {
        expires -1s;
        add_header Cache-Control "no-cache" always;
        add_header X-Content-Type-Options "nosniff" always;
        try_files $uri =404;
    }

"""
        code = """import subprocess,socket,time
from pathlib import Path
# Unchanged supervisor configuration is not restarted by update after stop.
status=subprocess.run(['supervisorctl','status','bookharbor-metadata'],capture_output=True,text=True)
if 'RUNNING' not in status.stdout:subprocess.run(['supervisorctl','start','bookharbor-metadata'],check=True)
for attempt in range(20):
 try:
  with socket.create_connection(('127.0.0.1',8004),timeout=1):pass
  break
 except OSError:
  if attempt==19:raise
  time.sleep(0.5)
p=Path('/etc/nginx/conf.d/talebook.conf');old=p.read_text();marker='    location ~ ^/(api|get|read|read-comic|opds|auth|books|media)/ {'
assert old.count(marker)==1
new=old.replace('book/[0-9]+/(?:edit|refer)', 'book/[0-9]+(?:/(?:edit|refer))?')
# Upgrade the previously managed regex too; an existing 8004 location is not sufficient.
new=new.replace('admin/metadata/enrichment)$ {','admin/metadata/enrichment(?:/sources/[0-9]+|/review)?)$ {')
new=new.replace('admin/metadata/enrichment(?:/sources/[0-9]+)?)$ {','admin/metadata/enrichment(?:/sources/[0-9]+|/review)?)$ {')
if 'proxy_pass http://127.0.0.1:8004;' not in new:new=new.replace(marker,ROUTE+marker)
if 'location ^~ /_i18n/' not in new:
 static='    location ^~ /_nuxt/ {';assert new.count(static)==1;new=new.replace(static,LOCALE_GUARD+static)
if 'location = /get/pcover {' not in new:new=new.replace(marker,COVER_ROUTE+marker)
if 'location ^~ /opds/ {' not in new:new=new.replace(marker,OPDS_ROUTE+marker)
if 'location = /opds {' not in new:new=new.replace(marker,OPDS_ROOT+marker)
if new!=old:p.write_text(new)
# The production Nginx master runs as talebook, unlike this root installer.
p.chmod(0o644)
subprocess.run(['nginx','-t'],check=True)
subprocess.run(['nginx','-s','reload'],check=True)
print('Only metadata API locations switched')
""".replace("OPDS_ROOT", repr(opds_root_route)).replace("OPDS_ROUTE", repr(opds_route)).replace("COVER_ROUTE", repr(cover_route)).replace("ROUTE", repr(route)).replace("LOCALE_GUARD", repr(locale_guard))
        print(inside(code), flush=True)
        if args.frontend:
            frontend = args.frontend.resolve()
            if not (frontend / "200.html").is_file():
                raise RuntimeError("Nuxt SPA output has no 200.html")
            archive = io.BytesIO()
            with tarfile.open(fileobj=archive, mode="w:gz") as bundle:
                for directory in ["_nuxt", "_i18n"]:
                    if not (frontend / directory).is_dir():
                        raise RuntimeError(f"Nuxt SPA output missing {directory}")
                    for source in (frontend / directory).rglob("*"):
                        if source.is_file() and not source.is_symlink():
                            bundle.add(source, arcname=source.relative_to(frontend).as_posix())
                for name in ["200.html", "404.html"]:
                    if (frontend / name).is_file():
                        bundle.add(frontend / name, arcname=name)
            frontend_code = """import sys,io,tarfile,shutil,json,re,os
from pathlib import Path
base=Path(BASE);root=Path('/run/talebook-release/current/app/dist').resolve();assert root.is_dir()
stage=base/'frontend-stage';stage.mkdir(exist_ok=True)
archive=tarfile.open(fileobj=io.BytesIO(sys.stdin.buffer.read()),mode='r:gz')
for member in archive.getmembers():
 assert member.isfile() and not member.issym() and not member.islnk()
 path=Path(member.name);assert not path.is_absolute() and '..' not in path.parts
 assert path.parts[0] in ['_nuxt','_i18n','200.html','404.html']
archive.extractall(stage,filter='data')
backup=base/'frontend-before';backup.mkdir(exist_ok=True)
present=[]
for name in ['index.html','200.html','404.html']:
 if (root/name).exists():shutil.copy2(root/name,backup/name);present.append(name)
(backup/'manifest.json').write_text(json.dumps(present))
for directory in ['_nuxt','_i18n']:shutil.copytree(stage/directory,root/directory,dirs_exist_ok=True)
# Archive extraction under a private umask creates 0700 parents. Public assets
# need explicit traversal/read permissions, regardless of their owner.
for directory in ['_nuxt','_i18n']:
 (root/directory).chmod(0o755)
 for path in (root/directory).rglob('*'):path.chmod(0o755 if path.is_dir() else 0o644)
old=(root/'index.html').read_text() if (root/'index.html').exists() else ''
version=re.search(r'data-talebook-version="([^"]+)"',old)
for source,target in [('200.html','200.html'),('200.html','index.html'),('404.html','404.html')]:
 if not (stage/source).exists():continue
 text=(stage/source).read_text()
 if version and 'data-talebook-version=' not in text:text=text.replace('<html','<html data-talebook-version="'+version[1]+'"',1)
 temporary=root/(target+'.metadata-next');temporary.write_text(text);temporary.chmod(0o644);os.replace(temporary,root/target)
shutil.rmtree(stage)
print('SPA entries and locale assets updated; Readest and old hashed assets retained')
""".replace("BASE", repr(maintenance))
            print(inside(frontend_code, archive.getvalue()), flush=True)
        after = json.loads(inside(state_code))
        assert after == before, "Import or reader process changed during deployment"
        release = """import sqlite3,os
os.setgroups([]);os.setgid(1000);os.setuid(1000)
c=sqlite3.connect('/data/books/library/metadata.db',timeout=1)
c.execute('DELETE FROM metadata_enrichment_lease WHERE id=1 AND owner=?',(FREEZE,));c.commit();c.close()
""".replace("FREEZE", repr(freeze))
        inside(release)
    except Exception:
        rollback = """import shutil,subprocess,sqlite3,os
from pathlib import Path
base=Path(BASE);shutil.copy2(base/'nginx-before.conf','/etc/nginx/conf.d/talebook.conf')
Path('/etc/nginx/conf.d/talebook.conf').chmod(0o644)
backup=base/'frontend-before'
if RESTORE_FRONTEND and (backup/'manifest.json').exists():
 import json
 root=Path('/run/talebook-release/current/app/dist').resolve()
 for name in json.loads((backup/'manifest.json').read_text()):
  assert name in ['index.html','200.html','404.html'];shutil.copy2(backup/name,root/name)
subprocess.run(['nginx','-t'],check=True);subprocess.run(['nginx','-s','reload'],check=True)
subprocess.run(['supervisorctl','stop','bookharbor-metadata'])
old=base/'supervisor-before.conf';p=Path('/etc/supervisor/conf.d/bookharbor-metadata.conf')
if old.exists():shutil.copy2(old,p)
else:p.unlink(missing_ok=True)
subprocess.run(['supervisorctl','reread']);subprocess.run(['supervisorctl','update','bookharbor-metadata'])
os.setgroups([]);os.setgid(1000);os.setuid(1000)
c=sqlite3.connect('/data/books/library/metadata.db',timeout=1)
if c.execute("SELECT count(*) FROM sqlite_master WHERE name='metadata_enrichment_lease'").fetchone()[0]:c.execute('DELETE FROM metadata_enrichment_lease WHERE id=1 AND owner=?',(FREEZE,))
c.commit();c.close()
""".replace("BASE", repr(maintenance)).replace("FREEZE", repr(freeze)).replace("RESTORE_FRONTEND", repr(bool(args.frontend)))
        print(inside(rollback), flush=True)
        raise
    receipt = {"revision": args.revision, "binary": target, "sha256": digest, "preserved_processes": after,
               "mode": "metadata-only", "local_interval_seconds": 1, "network_interval_seconds": 0,"source_default_interval_seconds":5,"network_max_concurrency":8}
    print(inside("from pathlib import Path;Path(" + repr(maintenance + "/receipt.json") + ").write_text(" + repr(json.dumps(receipt, indent=2)) + ")"), flush=True)
    print(json.dumps(receipt, indent=2), flush=True)


if __name__ == "__main__":
    main()
