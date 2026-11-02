#!/usr/bin/env python3
"""Native Linux integration check with a disposable DB and real Xray HTTP traffic.

Usage: python3 scripts/verify-low-write.py --panel /path/x-ui --assets /path/bin
Assets must contain xray-linux-<GOARCH> and the matching geodata files.
No RouterOS/TUN privileges are exercised by this check.
"""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import sqlite3
import subprocess
import tempfile
import threading
import time
import urllib.request


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


class Origin(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'low-write-check' * 4096)

    def log_message(self, *args):
        pass


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--panel', required=True, type=Path)
    parser.add_argument('--assets', required=True, type=Path)
    parser.add_argument('--seconds', type=int, default=70)
    args = parser.parse_args()
    assert args.seconds >= 65, 'Observe at least one minute persistence tick'
    panel = args.panel.resolve()
    with tempfile.TemporaryDirectory(prefix='xui-low-write-') as directory:
        root = Path(directory)
        assets = root / 'bin'
        shutil.copytree(args.assets, assets)
        database = root / 'db'
        database.mkdir()
        port, proxy_port, api_port = free_port(), free_port(), free_port()
        base_env = dict(os.environ, XUI_DB_FOLDER=str(database),
                        XUI_BIN_FOLDER=str(assets), XUI_LOG_FOLDER=str(root / 'logs'),
                        XUI_PORT=str(port), XUI_INIT_WEB_BASE_PATH='/',
                        XUI_DISABLE_METRICS_PERSISTENCE='true', XUI_ENABLE_FAIL2BAN='false')
        processes = []
        direct = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def launch(disabled):
            env = dict(base_env, XUI_DISABLE_TRAFFIC_STATS=str(disabled).lower())
            log = open(root / 'panel.log', 'ab')
            proc = subprocess.Popen([str(panel)], cwd=root, env=env,
                                    stdout=log, stderr=log, start_new_session=True)
            log.close()
            processes.append(proc)
            for _ in range(200):
                if proc.poll() is not None:
                    raise RuntimeError(f'Panel exited: {proc.returncode}; inspect isolated logs')
                try:
                    with direct.open(f'http://127.0.0.1:{port}/', timeout=1) as response:
                        assert response.status == 200
                    time.sleep(2)  # HTTP starts before main installs signal handlers.
                    return proc
                except OSError:
                    time.sleep(0.1)
            raise RuntimeError('Panel did not start')

        def stop(proc):
            proc.send_signal(signal.SIGTERM)
            try:
                assert proc.wait(timeout=30) == 0
            finally:
                if proc.poll() is None:
                    os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait()

        origin = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Origin)
        threading.Thread(target=origin.serve_forever, daemon=True).start()
        try:
            stop(launch(True))
            dbpath = database / 'x-ui.db'
            template = {
                'api': {'tag': 'api', 'services': ['HandlerService', 'StatsService', 'RoutingService']},
                'inbounds': [{'tag': 'api', 'listen': '127.0.0.1', 'port': api_port,
                              'protocol': 'tunnel', 'settings': {'rewriteAddress': '127.0.0.1'}}],
                'log': {'loglevel': 'none', 'access': 'none', 'error': 'none'},
                'outbounds': [{'tag': 'direct', 'protocol': 'freedom', 'settings': {}}],
                'routing': {'rules': [{'type': 'field', 'inboundTag': ['api'], 'outboundTag': 'api'}]},
                'policy': {'levels': {'0': {'statsUserUplink': True, 'statsUserDownlink': True}},
                           'system': {key: True for key in ['statsInboundUplink', 'statsInboundDownlink',
                                                           'statsOutboundUplink', 'statsOutboundDownlink']}},
                'stats': {},
            }
            with sqlite3.connect(dbpath) as db:
                for key, value in [('xrayTemplateConfig', json.dumps(template)), ('subEnable', 'false')]:
                    db.execute('DELETE FROM settings WHERE key=?', (key,))
                    db.execute('INSERT INTO settings(key,value) VALUES (?,?)', (key, value))
                db.execute('INSERT INTO inbounds(user_id,tag,enable,port,listen,protocol,settings,stream_settings,sniffing,up,down,total,expiry_time) '
                           'VALUES (1,?,1,?,\'127.0.0.1\',\'http\',\'{}\',\'{"network":"tcp","security":"none"}\',\'{}\',0,0,0,0)',
                           ('low-write-http', proxy_port))

            def transfer():
                # http.client always uses the supplied proxy, including for loopback destinations.
                import http.client
                conn = http.client.HTTPConnection('127.0.0.1', proxy_port, timeout=3)
                try:
                    conn.request('GET', f'http://127.0.0.1:{origin.server_port}/payload')
                    response = conn.getresponse()
                    assert response.status == 200 and len(response.read()) > 50000
                finally:
                    conn.close()

            def snapshot():
                with sqlite3.connect(dbpath) as db:
                    # Logical snapshot includes WAL-visible rows in every application table.
                    return '\n'.join(db.iterdump())

            def durable_files():
                return {path.name: hashlib.sha256(path.read_bytes()).hexdigest()
                        for path in database.glob('x-ui.db*')
                        if path.name.endswith(('.db', '-wal', '-journal'))}

            proc = launch(True)
            for _ in range(100):
                try:
                    transfer()
                    break
                except OSError:
                    time.sleep(0.1)
            else:
                raise RuntimeError('Xray HTTP inbound did not start')
            time.sleep(3)  # Let startup-only migrations/settings settle.
            before = snapshot()
            files_before = durable_files()
            start = time.monotonic()
            count = 0
            while time.monotonic() - start < args.seconds:
                transfer()
                count += 1
                time.sleep(1)
            assert snapshot() == before, 'SQLite changed during disabled-stat traffic window'
            assert durable_files() == files_before, 'SQLite DB/WAL/journal bytes changed during disabled-stat traffic window'
            stop(proc)
            assert not list(database.glob('system_metrics.gob*')), 'Metrics snapshot created'
            assert snapshot() == before, 'Shutdown changed SQLite'
            print(f'PASS: real Xray HTTP transfers={count}, observation={args.seconds}s; SQLite unchanged; no metrics on timer/shutdown', flush=True)

            proc = launch(False)
            for _ in range(15):
                transfer()
                time.sleep(1)
            with sqlite3.connect(dbpath) as db:
                up, down = db.execute('SELECT up,down FROM inbounds WHERE tag=?', ('low-write-http',)).fetchone()
                assert (up or 0) + (down or 0) > 0, 'Default traffic stats did not record bytes'
                outbound = db.execute('SELECT count(*) FROM outbound_traffics WHERE up+down>0').fetchone()[0]
                assert outbound > 0, 'Default outbound stats did not record bytes'
            stop(proc)
            assert not list(database.glob('system_metrics.gob*'))
            print('PASS: default inbound/outbound traffic accounting records bytes; Xray restarts and routes HTTP', flush=True)
        finally:
            origin.shutdown()
            for proc in processes:
                if proc.poll() is None:
                    os.killpg(proc.pid, signal.SIGTERM)
                    try:
                        proc.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        os.killpg(proc.pid, signal.SIGKILL)
                        proc.wait()


if __name__ == '__main__':
    main()
