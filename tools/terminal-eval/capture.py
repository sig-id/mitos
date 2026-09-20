#!/usr/bin/env python3
"""Record CLI startup screens only, with isolated setup and no model prompts.
Recordings remain local; do not commit transcripts or credential-bearing output.
"""
import argparse
import errno
import hashlib
import json
import os
import pathlib
import pty
import select
import shutil
import signal
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser()
parser.add_argument('output', type=pathlib.Path)
args = parser.parse_args()
args.output.mkdir(parents=True, exist_ok=True, mode=0o700)
os.chmod(args.output,0o700)
manifest = []
for agent, command in [('codex', 'codex'), ('claude', 'claude'), ('opencode', 'opencode'), ('pi', 'pi')]:
    executable = shutil.which(command)
    if not executable:
        manifest.append({'agent': agent, 'status': 'not installed'})
        continue
    with tempfile.TemporaryDirectory(prefix='mitos-capture-') as directory:
        root = pathlib.Path(directory)
        env = {k: v for k, v in os.environ.items() if k in ['PATH', 'HOME', 'LANG', 'LC_ALL']}
        env.update(TERM='xterm-256color', COLORTERM='truecolor',
                   CODEX_HOME=str(root/'codex'), CLAUDE_CONFIG_DIR=str(root/'claude'),
                   OPENCODE_CONFIG_DIR=str(root/'opencode'), PI_CODING_AGENT_DIR=str(root/'pi'),
                   XDG_CONFIG_HOME=str(root/'config'), XDG_DATA_HOME=str(root/'data'),
                   XDG_CACHE_HOME=str(root/'cache'), DISABLE_AUTOUPDATER='1')
        for path in ['codex', 'claude', 'opencode', 'pi', 'config', 'data', 'cache']:
            (root/path).mkdir()
        master, slave = pty.openpty()
        import fcntl, struct, termios
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 28, 100, 0, 0))
        def setup():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)
        proc = subprocess.Popen([executable], cwd=root, env=env, stdin=slave, stdout=slave, stderr=slave, preexec_fn=setup)
        os.close(slave)
        data, pending = bytearray(), bytearray()
        deadline = time.monotonic()+6
        responses = {b'\x1b[6n': b'\x1b[1;1R', b'\x1b[c': b'\x1b[?1;2c', b'\x1b[>c': b'\x1b[>0;0;0c',
                     b'\x1b]10;?\x07': b'\x1b]10;rgb:d8d8/dede/e9e9\x07', b'\x1b]11;?\x07': b'\x1b]11;rgb:1414/1717/1b1b\x07',
                     b'\x1b]10;?\x1b\\': b'\x1b]10;rgb:d8d8/dede/e9e9\x1b\\', b'\x1b]11;?\x1b\\': b'\x1b]11;rgb:1414/1717/1b1b\x1b\\'}
        try:
            while time.monotonic()<deadline and len(data)<256*1024:
                if not select.select([master], [], [], .1)[0]:
                    continue
                try: chunk=os.read(master, 65536)
                except OSError as exc:
                    if exc.errno==errno.EIO: break
                    raise
                if not chunk: break
                data.extend(chunk); pending.extend(chunk)
                for query, response in responses.items():
                    count=pending.count(query)
                    if count:
                        os.write(master, response*count)
                        pending=pending.replace(query,b'')
                pending=pending[-128:]
        finally:
            try: os.killpg(proc.pid,signal.SIGKILL)
            except ProcessLookupError: pass
            proc.wait(); os.close(master)
        (args.output/(agent+'.bin')).write_bytes(data)
        manifest.append({'agent':agent,'status':'startup only','bytes':len(data),'sha256':hashlib.sha256(data).hexdigest()})
(args.output/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
print(json.dumps(manifest,indent=2))
