"""Prepare a disposable native image and data directory for upgrade acceptance."""
import argparse
import json
import shutil
import subprocess
from pathlib import Path

def prepare(work, runtime_image, frontend):
    work.mkdir(parents=True, exist_ok=False)
    if not (frontend / 'index.html').is_file() and not (frontend / '200.html').is_file():
        raise ValueError('A built frontend is required')
    context = work / 'context'
    context.mkdir()
    shutil.copytree(frontend, context / 'ui')
    if not (context / 'ui/index.html').exists():
        shutil.copy(context / 'ui/200.html', context / 'ui/index.html')
    (context / 'Dockerfile').write_text('FROM ' + runtime_image + '\nCOPY ui/ /var/www/talebook/app/dist/\nRUN test -x /var/www/talebook/booksvr && test ! -e /var/www/talebook/server.py\n')
    subprocess.run(['docker', 'build', '-t', 'talebook:tb213-smoke', str(context)], check=True)
    (work / 'data').mkdir()
    print(json.dumps({'image':'talebook:tb213-smoke','data':str(work/'data'),'backend':'booksvr'}))

if __name__ == '__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--work',type=Path,required=True)
    parser.add_argument('--runtime-image',default='ghcr.io/leonzy/bookharbor:main')
    parser.add_argument('--frontend',type=Path,required=True)
    args=parser.parse_args()
    prepare(args.work.resolve(),args.runtime_image,args.frontend.resolve())
