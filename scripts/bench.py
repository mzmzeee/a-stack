#!/usr/bin/env python3
"""Heavy-use load and profiling harness for a-stack. Standard library only.

One command builds the current source, makes a disposable "weeks of heavy use"
fixture over HTTP, runs real CLI processes at several concurrency levels with
emulated browser pollers, profiles the in-process service, runs the Go fixture
benchmarks and writes raw data to --out. It never uses existing a-stack state:
the state directory is a new private directory inside --out, and it is deleted
at the end unless --keep is given.

    python3 scripts/bench.py --out /abs/dir            # full run (~25 min)
    python3 scripts/bench.py --out /abs/dir --quick    # small smoke run

For an A/B run, --keep a fixture once, then point --src at a patched copy of
the source tree and --reuse at that fixture. --plans-only with --reuse only
writes query plans for the kept fixture.

--out must be an absolute directory outside any Git checkout and on a
persistent filesystem (not tmpfs) so fsync costs are real.
"""
import argparse
import base64
import collections
import hashlib
import http.client
import json
import os
from pathlib import Path
import random
import secrets
import shutil
import signal
import sqlite3
import subprocess
import sys
import threading
import time
from urllib.parse import urlsplit

REPO = Path(__file__).resolve().parent.parent  # replaced by --src
CLK = os.sysconf('SC_CLK_TCK')


def check(cond, msg):
    if not cond:
        raise AssertionError(msg)


def pct(values, p):
    if not values:
        return None
    s = sorted(values)
    return round(s[min(len(s) - 1, max(0, int(round(p / 100 * len(s) + 0.5)) - 1))], 3)


def stats(values):
    return {'n': len(values), 'p50': pct(values, 50), 'p95': pct(values, 95), 'p99': pct(values, 99),
            'max': round(max(values), 3) if values else None, 'mean': round(sum(values) / len(values), 3) if values else None}


def token():
    return base64.urlsafe_b64encode(secrets.token_bytes(32)).rstrip(b'=').decode()


def write_private(path, value):
    tmp = Path(str(path) + '.new')
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as f:
        json.dump(value, f)
    tmp.replace(path)


class ApiError(Exception):
    def __init__(self, status, code, body):
        super().__init__(f'{status} {code}')
        self.status, self.code, self.body = status, code, body


class Api:
    """One keep-alive connection per thread, like a long-lived client."""

    def __init__(self, bench):
        self.bench, self.local = bench, threading.local()

    def conn(self):
        c = getattr(self.local, 'c', None)
        if c is None:
            c = self.local.c = http.client.HTTPConnection('127.0.0.1', self.bench.port, timeout=60)
        return c

    def raw(self, path, payload, tok, headers=None, method='POST'):
        body = json.dumps(payload).encode() if payload is not None else None
        h = {'Authorization': 'Bearer ' + tok, 'X-A-Stack-Instance': self.bench.instance}
        if body is not None:
            h['Content-Type'] = 'application/json'
        h.update(headers or {})
        for attempt in range(2):
            c = self.conn()
            try:
                c.request(method, path, body, h)
                r = c.getresponse()
                data = r.read()
                return r.status, data, r
            except (http.client.RemoteDisconnected, ConnectionResetError, BrokenPipeError, http.client.CannotSendRequest):
                c.close()
                self.local.c = None
                if attempt:
                    raise

    def call(self, path, payload, tok):
        status, data, _ = self.raw(path, payload, tok)
        doc = json.loads(data) if data else {}
        if status != 200:
            raise ApiError(status, doc.get('error', {}).get('code'), doc)
        return doc['data']


class Bench:
    def __init__(self, args):
        self.args = args
        self.out = Path(args.out)
        self.out.mkdir(parents=True, exist_ok=True)
        os.chmod(self.out, 0o700)
        if args.reuse:
            self.fixture = Path(os.path.realpath(args.reuse))
        else:
            self.fixture = Path(os.path.realpath(self.out / f'fixture-{os.getpid()}'))
            self.fixture.mkdir(mode=0o700)
        self.state = self.fixture / 'state'
        self.creds = self.fixture / 'credentials'
        self.creds.mkdir(mode=0o700, exist_ok=True)
        self.backups = self.fixture / 'backups'
        self.backups.mkdir(mode=0o700, exist_ok=True)
        self.cursors = self.fixture / 'cursors'
        self.cursors.mkdir(mode=0o700, exist_ok=True)
        self.project = self.fixture / 'project'
        self.project.mkdir(exist_ok=True)
        self.bin = self.out / 'bin'
        self.bin.mkdir(exist_ok=True)
        self.results = {'args': vars(args), 'started': time.strftime('%Y-%m-%dT%H:%M:%S%z')}
        self.api = Api(self)
        self.opseq = 0
        self.oplock = threading.Lock()
        self.rng = random.Random(args.seed)
        self.proc = None
        self.run_id = secrets.token_hex(3)

    # ---------- infrastructure ----------
    def op(self, prefix='fx'):
        with self.oplock:
            self.opseq += 1
            return f'{prefix}-{self.opseq:010d}'

    def build(self):
        t = time.monotonic()
        subprocess.run(['go', 'build', '-o', str(self.bin / 'a-stack'), './cmd/a-stack'], cwd=REPO, check=True)
        subprocess.run(['go', 'test', '-c', '-tags', 'bench', '-o', str(self.bin / 'svc.test'), './internal/stack'], cwd=REPO, check=True)
        self.cli_bin = str(self.bin / 'a-stack')
        sha = hashlib.sha256(Path(self.cli_bin).read_bytes()).hexdigest()
        rev = subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=REPO, capture_output=True, text=True).stdout.strip()
        self.results['build'] = {'seconds': round(time.monotonic() - t, 1), 'cli_sha256': sha, 'git_head': rev,
                                 'cli_bytes': Path(self.cli_bin).stat().st_size,
                                 'go': subprocess.run(['go', 'version'], capture_output=True, text=True).stdout.strip()}

    def machine(self):
        """Power state matters: on battery with the powersave profile every span is about 2x slower."""
        def read(path):
            try:
                return Path(path).read_text().strip()
            except OSError:
                return None
        supplies = {p.name: read(p / 'online') or read(p / 'status') for p in Path('/sys/class/power_supply').glob('*')}
        self.results['machine'] = {'cpus': os.cpu_count(), 'power_supply': supplies, 'platform_profile': read('/sys/firmware/acpi/platform_profile'),
                                   'governor': read('/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor'),
                                   'loadavg_start': read('/proc/loadavg'), 'kernel': read('/proc/sys/kernel/osrelease')}

    def start(self):
        env = dict(os.environ, ASTACK_BENCH_STATE=str(self.state), ASTACK_BENCH_OUT=str(self.out),
                   ASTACK_BENCH_MAX_SECONDS=str(self.args.max_seconds), ASTACK_BENCH_BLOCK_RATE=str(self.args.block_rate))
        self.proc = subprocess.Popen([str(self.bin / 'svc.test'), '-test.run', '^TestBenchServe$', '-test.timeout', '0'],
                                     stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=env)
        line = self.proc.stdout.readline()
        ready = json.loads(line)['data']
        self.url, self.pprof, self.pid = ready['url'], ready['pprof'], ready['pid']
        self.port = urlsplit(self.url).port
        self.instance = json.loads((self.state / 'service.json').read_text())['instance_id']
        self.op_token = json.loads((self.state / 'operator.json').read_text())['token']
        self.op_file = self.state / 'operator.json'

    def stop(self):
        if self.proc and self.proc.poll() is None:
            self.proc.send_signal(signal.SIGTERM)
            try:
                self.proc.wait(timeout=20)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait()
        self.proc = None

    def proc_sample(self):
        with open(f'/proc/{self.pid}/stat') as f:
            fields = f.read().rsplit(')', 1)[1].split()
        cpu = (int(fields[11]) + int(fields[12])) / CLK
        status = {}
        with open(f'/proc/{self.pid}/status') as f:
            for line in f:
                k, _, v = line.partition(':')
                if k in ('VmRSS', 'VmHWM', 'Threads'):
                    status[k] = int(v.split()[0])
        sizes = {}
        for suffix in ['', '-wal', '-shm']:
            try:
                sizes['db' + (suffix or '_main')] = (self.state / ('state.db' + suffix)).stat().st_size
            except FileNotFoundError:
                sizes['db' + (suffix or '_main')] = 0
        return {'t': time.time(), 'cpu_s': cpu, 'rss_kb': status.get('VmRSS'), 'hwm_kb': status.get('VmHWM'),
                'threads': status.get('Threads'), **sizes}

    def sampler(self, stop_event, rows):
        while not stop_event.is_set():
            try:
                rows.append(self.proc_sample())
            except (FileNotFoundError, ProcessLookupError):
                return
            stop_event.wait(1)

    def _storage(self):
        status, data, _ = self.api.raw('/v1/storage', None, self.op_token, method='GET')
        return json.loads(data)['data']

    def cli(self, args, cred=None, kind='agent', stdin=None):
        """Runs one real CLI process. Returns (ms, rc, stdout, stderr, rusage)."""
        cmd = [self.cli_bin, *map(str, args), '--state-dir', str(self.state)]
        if cred:
            cmd += ['--as', kind, '--credential-file', str(cred)]
        t = time.perf_counter()
        p = subprocess.Popen(cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        out = p.stdout.read()
        err = p.stderr.read()
        _, status, ru = os.wait4(p.pid, 0)
        ms = (time.perf_counter() - t) * 1000
        p.returncode = os.waitstatus_to_exitcode(status)
        p.stdout.close()
        p.stderr.close()
        return ms, p.returncode, out, err, ru

    # ---------- fixture ----------
    def ctx(self, session):
        return {'workspace_id': self.workspace, 'session_id': session}

    def register(self, session, name, by):
        tok = token()
        receipt = self.api.call('/v1/agent/register', {**self.ctx(session), 'operation_id': self.op(), 'name': name,
                                                       'credential_hash': hashlib.sha256(tok.encode()).hexdigest()}, by)
        path = self.creds / f'{session[:8]}-{name}.json'
        write_private(path, {'kind': 'agent', 'token': tok})
        return {'id': receipt['resource_id'], 'name': name, 'token': tok, 'file': path}

    def make_session(self, name, n_agents, prefix):
        s = self.api.call('/v1/session/create', {'operation_id': self.op(), 'workspace_id': self.workspace, 'name': name}, self.op_token)['resource_id']
        orch = self.register(s, f'{prefix}01-orchestrator', self.op_token)
        self.api.call('/v1/session/appoint', {**self.ctx(s), 'operation_id': self.op(), 'expected_revision': 1, 'agent_id': orch['id']}, self.op_token)
        agents = [orch] + [self.register(s, f'{prefix}{i + 2:02d}-worker', orch['token']) for i in range(n_agents - 1)]
        return s, agents

    def parallel(self, items, fn, workers):
        items = list(items)
        errors = []

        def run(chunk):
            try:
                for item in chunk:
                    fn(item)
            except Exception as e:  # surfaced below
                errors.append(e)
        threads = [threading.Thread(target=run, args=(items[i::workers],)) for i in range(workers)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        if errors:
            raise errors[0]

    def populate(self, session, agents, n_tasks, n_messages, label, growth=None):
        """Builds Tasks, a dependency graph, assignments, history and mail."""
        rng = random.Random(self.args.seed + n_tasks)
        orch = agents[0]
        timing = collections.defaultdict(list)
        tasks = []
        t0 = time.monotonic()
        for i in range(n_tasks):
            t = time.perf_counter()
            tid = self.api.call('/v1/task/create', {**self.ctx(session), 'operation_id': self.op(), 'title': f'{label} task {i:05d}: implement part {i}',
                                                    'description': 'Synthetic description. ' * rng.randint(2, 40), 'ticket_ref': f'#{label}-{i}'}, orch['token'])['resource_id']
            timing['task_create'].append((time.perf_counter() - t) * 1000)
            tasks.append({'id': tid, 'rev': 1, 'assignees': [], 'progress': 'waiting', 'i': i})
            if growth is not None and (i + 1) % 1000 == 0:
                growth.append({'phase': f'{label}-tasks', 'count': i + 1, 't': time.monotonic() - t0, **self._storage()})
        graph_rev = self.api.call('/v1/session/inspect', self.ctx(session), orch['token'])['graph_revision']
        # Realistic DAG: each Task depends on 0-3 recent earlier Tasks (a long chain of
        # workstreams with fan-in and fan-out); edges only point forward, so no cycles.
        edges = 0
        for task in tasks[1:]:
            k = rng.choices([0, 1, 2, 3], weights=[25, 40, 25, 10])[0]
            window = tasks[max(0, task['i'] - 60):task['i']]
            for prior in rng.sample(window, min(k, len(window))):
                t = time.perf_counter()
                self.api.call('/v1/dependency/add', {**self.ctx(session), 'operation_id': self.op(), 'expected_graph_revision': graph_rev,
                                                     'prerequisite_id': prior['id'], 'dependent_id': task['id']}, orch['token'])
                timing['dependency_add'].append((time.perf_counter() - t) * 1000)
                graph_rev += 1
                edges += 1
        workers = agents[1:]
        for task in tasks:
            r = rng.random()
            n = 0 if r < 0.05 else 1 if r < 0.55 else 2 if r < 0.85 else 3
            chosen = rng.sample(workers, min(n, len(workers)))
            if rng.random() < 0.08:
                chosen.append(orch)
            task['assignees'] = chosen

        def assign(task):
            for a in task['assignees']:
                t = time.perf_counter()
                self.api.call('/v1/task/assign', {**self.ctx(session), 'operation_id': self.op(), 'task_id': task['id'],
                                                  'expected_revision': task['rev'], 'agent_id': a['id']}, orch['token'])
                timing['task_assign'].append((time.perf_counter() - t) * 1000)
                task['rev'] += 1
        self.parallel(tasks, assign, self.args.writers)

        def history(task):
            if not task['assignees']:
                return
            r = random.Random(task['i'])

            def change(path, extra):
                a = r.choice(task['assignees'])
                t = time.perf_counter()
                self.api.call(path, {**self.ctx(session), 'operation_id': self.op(), 'task_id': task['id'], 'expected_revision': task['rev'], **extra}, a['token'])
                timing[path.rsplit('/', 1)[-1]].append((time.perf_counter() - t) * 1000)
                task['rev'] += 1
            for step in range(r.randint(1, 8)):
                change('/v1/task/status', {'progress': 'working', 'reason': f'Step {step}: working through the plan'})
                if r.random() < 0.6:
                    change('/v1/task/report', {'report': f'Report v{step}. ' + 'Progress detail line. ' * r.randint(3, 60)})
            if r.random() < 0.03:
                change('/v1/task/attention/set', {'question': 'Which option should we take for this part?'})
            if r.random() < 0.15:
                change('/v1/task/reference/add', {'location': f'/home/user/project/docs/part-{task["i"]}.md', 'reference_revision': 'abc123'})
            fate = r.random()
            if fate < 0.35:
                change('/v1/task/status', {'progress': 'finished', 'summary': 'Done and verified.'})
                task['progress'] = 'finished'
                if r.random() < 0.1:
                    change('/v1/task/reopen', {'reason': 'Regression found'})
                    task['progress'] = 'waiting'
            elif fate < 0.40:
                change('/v1/task/status', {'progress': 'cancelled', 'reason': 'No longer needed'})
                task['progress'] = 'cancelled'
            else:
                task['progress'] = 'working'
        self.parallel(tasks, history, self.args.writers)
        tasks_seconds = time.monotonic() - t0
        # Mail: skewed across Tasks (hot Tasks get much more), 1-2 mentions of other assignees.
        mailable = [t for t in tasks if t['assignees']]
        weights = [1 / (1 + j) ** 0.8 for j in range(len(mailable))]
        rng.shuffle(mailable)
        plan = rng.choices(mailable, weights=weights, k=n_messages)
        counter = {'n': 0}
        lock = threading.Lock()
        t1 = time.monotonic()

        def send(item):
            j, task = item
            r = random.Random(j)
            sender = r.choice(task['assignees'])
            others = [a for a in task['assignees'] if a is not sender]
            names = r.sample(others, min(len(others), r.choice([0, 1, 1, 2])))
            body = ' '.join('@' + a['name'] for a in names) + ' ' + 'Status update: finished the step, next one starting. ' * r.randint(1, 6)
            t = time.perf_counter()
            self.api.call('/v1/mail/send', {**self.ctx(session), 'operation_id': self.op(), 'task_id': task['id'], 'body': body.strip()}, sender['token'])
            timing['mail_send'].append((time.perf_counter() - t) * 1000)
            with lock:
                counter['n'] += 1
                n = counter['n']
            if growth is not None and n % 10000 == 0:
                growth.append({'phase': f'{label}-mail', 'count': n, 't': time.monotonic() - t1, **self._storage()})
        self.parallel(list(enumerate(plan)), send, self.args.writers)
        mail_counts = collections.Counter(t['id'] for t in plan)
        for task in tasks:
            task['messages'] = mail_counts.get(task['id'], 0)
        return tasks, {'tasks': n_tasks, 'dependencies': edges, 'assignments': sum(len(t['assignees']) for t in tasks),
                       'messages': n_messages, 'tasks_phase_s': round(tasks_seconds, 1), 'mail_phase_s': round(time.monotonic() - t1, 1),
                       'timing_ms': {k: stats(v) for k, v in timing.items()}, 'dependency_add_ms_by_quartile': [stats(q) for q in quartiles(timing['dependency_add'])]}

    def backup(self, label):
        dest = self.backups / f'{label}.sqlite'
        before = self._storage()
        ms, rc, out, err, _ = self.cli(['storage', 'backup', '--destination', dest, '--json'])
        check(rc == 0, 'backup failed: ' + err.decode()[:200])
        return {'label': label, 'ms': round(ms, 1), 'backup_bytes': dest.stat().st_size, 'live': before, 'path': dest}

    def save_fixture(self):
        def names(t):
            return [a['name'] for a in t['assignees']]
        write_private(self.fixture / 'fixture-state.json', {
            'workspace': self.workspace, 'session': self.session,
            'agents': [{k: str(v) for k, v in a.items()} for a in self.agents],
            'tasks': [{**{k: v for k, v in t.items() if k != 'assignees'}, 'assignees': names(t)} for t in self.tasks],
            'fixture': self.results.get('fixture'), 'growth': self.results.get('growth'), 'backups': [{**b, 'path': str(b['path'])} for b in self.results.get('backups', [])]})

    def load_fixture(self):
        doc = json.loads((self.fixture / 'fixture-state.json').read_text())
        self.workspace, self.session = doc['workspace'], doc['session']
        self.agents = [{**a, 'file': Path(a['file'])} for a in doc['agents']]
        by_name = {a['name']: a for a in self.agents}
        self.tasks = [{**t, 'assignees': [by_name[n] for n in t['assignees']]} for t in doc['tasks']]
        self.results['reused_fixture'] = {'fixture': doc['fixture'], 'growth': doc['growth']}
        self.results['backups'] = [{**b, 'path': Path(b['path'])} for b in doc['backups']]

    def build_fixture(self):
        a = self.args
        self.workspace = self.api.call('/v1/workspace/create', {'operation_id': self.op(), 'path': str(self.project)}, self.op_token)['resource_id']
        growth = []
        self.results['growth'] = growth
        fx = {}
        # Closed Sessions: earlier weeks of work, kept with their history.
        for k in range(a.closed_sessions):
            s, agents = self.make_session(f'Closed week {k + 1}', 6, f'c{k}-')
            _, info = self.populate(s, agents, a.closed_tasks, a.closed_messages, f'c{k}')
            rev = self.api.call('/v1/session/inspect', self.ctx(s), self.op_token)['revision']
            self.api.call('/v1/session/close', {**self.ctx(s), 'operation_id': self.op(), 'expected_revision': rev}, self.op_token)
            fx[f'closed_{k}'] = info
        # A small second open Session.
        s2, agents2 = self.make_session('Side session', 4, 's-')
        _, fx['side'] = self.populate(s2, agents2, a.side_tasks, a.side_messages, 'side')
        growth.append({'phase': 'before-busy', 'count': 0, 't': 0, **self._storage()})
        self.results['backups'] = [self.backup('before-busy')]
        # The busy Session.
        self.session, self.agents = self.make_session('Busy session', a.agents, '')
        self.tasks, fx['busy'] = self.populate(self.session, self.agents, a.tasks, a.messages, 'busy', growth)
        self.results['fixture'] = fx
        self.results['backups'].append(self.backup('after-busy'))

    # ---------- analysis of a completed backup ----------
    def analyse(self, path):
        db = sqlite3.connect(Path(path).as_uri() + '?mode=ro', uri=True)
        result = {'analysis_sqlite': sqlite3.sqlite_version}
        tables = ['tasks', 'task_content', 'task_history', 'task_assignees', 'dependencies', 'graph_history', 'task_references', 'task_questions',
                  'mail_messages', 'mail_mentions', 'operation_receipts', 'capabilities', 'agents', 'sessions', 'lifecycle_history']
        result['rows'] = {t: db.execute(f'SELECT count(*) FROM {t}').fetchone()[0] for t in tables}
        result['notice_states'] = dict(db.execute('SELECT notice_state,count(*) FROM mail_mentions GROUP BY 1'))
        result['mail_body_bytes'] = db.execute('SELECT sum(length(CAST(body AS BLOB))) FROM mail_messages').fetchone()[0]
        result['content_body_bytes'] = db.execute('SELECT sum(length(CAST(body AS BLOB))) FROM task_content').fetchone()[0]
        result['objects'] = {n: s for n, s in db.execute('SELECT name,sum(pgsize) FROM dbstat GROUP BY name ORDER BY 2 DESC')}
        result['page_size'] = db.execute('PRAGMA page_size').fetchone()[0]
        result['stat1'] = bool(db.execute("SELECT count(*) FROM sqlite_schema WHERE name='sqlite_stat1'").fetchone()[0])
        # Representative IDs for EXPLAIN QUERY PLAN.
        sess = self.session
        task = max((t for t in self.tasks if t['assignees']), key=lambda t: t['messages'])
        agent = self.busiest_agent()
        msg = db.execute('SELECT id FROM mail_messages WHERE task_id=? LIMIT 1', (task['id'],)).fetchone()[0]
        tok = hashlib.sha256(b'x').hexdigest()
        from_internal = __import__('re')
        first_task, last_task = self.tasks[0]['id'], self.tasks[-1]['id']
        task_columns = (REPO / 'internal/stack/tasks.go').read_text()
        cols = from_internal.search(r'const taskColumns = `(.*?)`', task_columns, from_internal.S).group(1)
        old_cycle_sql = ('WITH RECURSIVE reachable(id) AS (SELECT ? UNION SELECT d.dependent_id FROM dependencies d JOIN reachable p '
                         'ON d.prerequisite_id=p.id WHERE d.session_id=?) SELECT count(*) FROM reachable WHERE id=?')
        found = from_internal.search(r'const cycleCheckSQL = `(.*?)`', task_columns, from_internal.S)
        cycle_sql = found.group(1) if found else old_cycle_sql
        queries = {
            'authenticate': ('SELECT kind,subject_id,session_id FROM capabilities WHERE token_hash=?', (tok,)),
            'load_session': ('SELECT id,workspace_id,name,closed,orchestrator_id,revision,read_revision,graph_revision,created_at,updated_at FROM sessions WHERE id=?', (sess,)),
            'member_check': ('SELECT count(*) FROM agents WHERE id=? AND session_id=? AND workspace_id=?', (agent['id'], sess, self.workspace)),
            'task_in_session (taskColumns)': ('SELECT ' + cols + ' FROM tasks t WHERE t.id=? AND t.session_id=?', (task['id'], sess)),
            'snapshot_counts': ('SELECT (SELECT count(*) FROM tasks WHERE session_id=?),(SELECT count(*) FROM agents WHERE session_id=?),(SELECT count(*) FROM dependencies WHERE session_id=?),(SELECT count(*) FROM task_assignees WHERE session_id=?)', (sess,) * 4),
            'page_tasks_ids': ('SELECT id FROM tasks WHERE session_id=? AND id>? ORDER BY id LIMIT ?', (sess, '', 101)),
            'page_assignments_ids': ("SELECT task_id||'/'||agent_id FROM task_assignees WHERE session_id=? AND (task_id,agent_id)>(?,?) ORDER BY task_id,agent_id LIMIT ?", (sess, '', '', 101)),
            'page_dependencies_ids': ('SELECT id FROM dependencies WHERE session_id=? AND id>? ORDER BY id LIMIT ?', (sess, '', 101)),
            'dependency_item': ('SELECT d.id,d.prerequisite_id,d.dependent_id,p.progress,n.progress,d.created_by,d.created_at FROM dependencies d JOIN tasks p ON p.id=d.prerequisite_id JOIN tasks n ON n.id=d.dependent_id WHERE d.id=? AND d.session_id=?', ('x', sess)),
            'assignment_item': ('SELECT m.task_id,m.agent_id,a.name,m.added_by,m.added_at FROM task_assignees m JOIN agents a ON a.id=m.agent_id WHERE m.task_id=? AND m.agent_id=? AND m.session_id=?', (task['id'], agent['id'], sess)),
            'history_ids': ('SELECT sequence FROM task_history WHERE task_id=? AND sequence>? ORDER BY sequence LIMIT ?', (task['id'], 0, 51)),
            'mail_check_tasks': ('SELECT task_id FROM task_assignees WHERE session_id=? AND agent_id=? AND task_id>? ORDER BY task_id LIMIT ?', (sess, agent['id'], '', 51)),
            'assignee_check': ('SELECT count(*) FROM task_assignees WHERE task_id=? AND agent_id=?', (task['id'], agent['id'])),
            'mail_position': ('SELECT 1 FROM mail_messages WHERE task_id=? AND sequence=?', (task['id'], 5)),
            'mail_page_seqs': ('SELECT sequence FROM mail_messages WHERE task_id=? AND sequence>? ORDER BY sequence LIMIT ?', (task['id'], 0, 101)),
            'mail_message': ('SELECT id,task_id,sequence,sender_id,received_at,body FROM mail_messages WHERE task_id=? AND sequence=?', (task['id'], 1)),
            'mail_mentions_of_message': ('SELECT recipient_id FROM mail_mentions WHERE message_id=? ORDER BY recipient_id', (msg,)),
            'mail_next_sequence': ('SELECT COALESCE(MAX(sequence),0)+1 FROM mail_messages WHERE task_id=?', (task['id'],)),
            'resolve_mention': ('SELECT a.id FROM task_assignees m JOIN agents a ON a.id=m.agent_id WHERE m.task_id=? AND a.name=?', (task['id'], agent['name'])),
            'mail_replay_lookup': ('SELECT task_id FROM mail_messages WHERE id=?', (msg,)),
            'receipt_lookup': ('SELECT digest,operation_id,resource_id,revision,outcome,body_ref,received_at FROM operation_receipts WHERE actor=? AND operation_id=?', ('agent:' + agent['id'], 'x')),
            'dependency_cycle_check': (cycle_sql, (first_task, sess, last_task)),
            'dependency_cycle_check_v0.1.2': (old_cycle_sql, (first_task, sess, last_task)),
            'dependency_cycle_check_late_task': (cycle_sql, (self.tasks[-200]['id'], sess, first_task)),
            'mail_check_assigned_tasks_join': ('SELECT x.task_id FROM task_assignees x WHERE x.session_id=? AND x.agent_id=? AND x.task_id>? ORDER BY x.task_id LIMIT ?', (sess, agent['id'], '', 101)),
            # Harness-only batched reads: one statement per page or per collection instead of one per item.
            'page_tasks_batched (harness-only)': ('SELECT ' + cols + ' FROM tasks t WHERE t.session_id=? AND t.id>? ORDER BY t.id LIMIT ?', (sess, '', 101)),
            'collection_tasks_all (harness-only)': ('SELECT ' + cols + ' FROM tasks t WHERE t.session_id=? ORDER BY t.id', (sess,)),
            'collection_assignments_all (harness-only)': ('SELECT m.task_id,m.agent_id,a.name,m.added_by,m.added_at FROM task_assignees m JOIN agents a ON a.id=m.agent_id WHERE m.session_id=? ORDER BY m.task_id,m.agent_id', (sess,)),
            'collection_dependencies_all (harness-only)': ('SELECT d.id,d.prerequisite_id,d.dependent_id,p.progress,n.progress,d.created_by,d.created_at FROM dependencies d JOIN tasks p ON p.id=d.prerequisite_id JOIN tasks n ON n.id=d.dependent_id WHERE d.session_id=? ORDER BY d.id', (sess,)),
            'mail_page_one_query (harness-only)': ('SELECT m.id,m.sequence,m.sender_id,m.received_at,m.body,(SELECT group_concat(recipient_id) FROM mail_mentions n WHERE n.message_id=m.id) FROM mail_messages m WHERE m.task_id=? AND m.sequence>? ORDER BY m.sequence LIMIT ?', (task['id'], 0, 101)),
            'suppress_mentions_on_unassign': ("UPDATE mail_mentions SET notice_state='suppressed',state_at='' WHERE task_id=? AND recipient_id=? AND notice_state IN ('pending','submitting')", (task['id'], agent['id'])),
            'notifier_next_target': ("""SELECT a.id,a.session_id,a.revision,COALESCE(a.binding,'') FROM agents a JOIN sessions s ON s.id=a.session_id
 LEFT JOIN notice_targets d ON d.session_id=a.session_id AND d.recipient_id=a.id
 WHERE s.closed=0 AND (?='' OR s.id=?) AND COALESCE(d.next_attempt,0)<=?
 AND NOT EXISTS(SELECT 1 FROM notice_batches b WHERE b.session_id=s.id AND b.recipient_id=a.id AND b.state='submitting')
 AND EXISTS(SELECT 1 FROM mail_mentions m JOIN task_assignees x ON x.task_id=m.task_id AND x.agent_id=m.recipient_id
 WHERE m.recipient_id=a.id AND x.session_id=s.id AND m.notice_state='pending')
 ORDER BY COALESCE(d.updated_at,''),a.id LIMIT 1""", ('', '', 0)),
            'notifier_claim': ("""SELECT m.message_id FROM mail_mentions m JOIN task_assignees x ON x.task_id=m.task_id AND x.agent_id=m.recipient_id
 WHERE x.session_id=? AND m.recipient_id=? AND m.notice_state='pending' ORDER BY m.message_id LIMIT 500""", (sess, agent['id'])),
        }
        plans = {}
        for name, (sql, params) in queries.items():
            try:
                plans[name] = [row[3] for row in db.execute('EXPLAIN QUERY PLAN ' + sql, params)]
            except sqlite3.Error as e:
                plans[name] = ['error: ' + str(e)]
        result['query_plans'] = plans
        self.plan_queries = queries
        # Timed full scans that indexes avoid or not (read-only, analysis engine).
        timed = {}
        for name in ['task_in_session (taskColumns)', 'mail_check_tasks', 'dependency_cycle_check', 'dependency_cycle_check_v0.1.2', 'dependency_cycle_check_late_task', 'snapshot_counts', 'mail_next_sequence', 'notifier_next_target', 'notifier_claim']:
            sql, params = queries[name]
            samples = []
            for _ in range(30):
                t = time.perf_counter()
                db.execute(sql, params).fetchall()
                samples.append((time.perf_counter() - t) * 1000)
            timed[name] = stats(samples[5:])
        result['query_ms_analysis_engine'] = timed
        db.close()
        return result

    def engine_plans(self, backup_path):
        """Query plans and timings with the service's bundled SQLite, before and after ANALYZE."""
        out = {}
        for analyze in (False, True):
            copy = self.fixture / f'plandb-{self.run_id}-{int(analyze)}'
            copy.mkdir(mode=0o700)
            db = copy / 'state.db'
            shutil.copy(backup_path, db)
            os.chmod(db, 0o600)
            spec, dest = copy / 'queries.json', copy / 'plans.json'
            write_private(spec, {'database': str(db), 'analyze': analyze, 'out': str(dest), 'runs': 25,
                                 'queries': {k: {'sql': q, 'params': list(p)} for k, (q, p) in self.plan_queries.items()}})
            p = subprocess.run(['go', 'test', '-tags', 'bench', '-count', '1', '-run', '^TestBenchQueryPlans$', './internal/stack'], cwd=REPO,
                               env=dict(os.environ, ASTACK_BENCH_QUERIES=str(spec)), capture_output=True, text=True, timeout=1800)
            check(p.returncode == 0, 'query plans failed: ' + p.stdout[-2000:] + p.stderr[-2000:])
            out['analyzed' if analyze else 'bundled'] = json.loads(dest.read_text())
            shutil.rmtree(copy, ignore_errors=True)
        (self.out / 'query-plans-bundled.json').write_text(json.dumps(out, indent=1))
        self.results['engine_plans'] = 'query-plans-bundled.json'

    def busiest_agent(self):
        counts = collections.Counter(a['id'] for t in self.tasks for a in t['assignees'])
        aid = counts.most_common(1)[0][0]
        return next(a for a in self.agents if a['id'] == aid)

    # ---------- micro measurements ----------
    def micro(self):
        r = {}
        def quiet(args, cred=None, kind='agent', n=self.args.samples):
            return [self.cli(args, cred, kind)[0] for _ in range(n + 3)][3:]
        r['version_ms'] = stats(quiet(['version']))
        r['health_operator_ms'] = stats(quiet(['health'], self.op_file, 'operator'))
        agent = self.busiest_agent()
        r['health_agent_ms'] = stats(quiet(['health'], agent['file']))
        # GODEBUG init trace of one start.
        p = subprocess.run([self.cli_bin, 'version'], env=dict(os.environ, GODEBUG='inittrace=1'), capture_output=True, text=True)
        inits = []
        for line in p.stderr.splitlines():
            parts = line.split()
            if parts[:1] == ['init'] and 'ms' in parts:
                inits.append({'pkg': parts[1], 'ms': float(parts[4]), 'bytes': int(parts[7]), 'allocs': int(parts[9])})
        r['init_total_ms'] = round(sum(i['ms'] for i in inits), 3)
        r['init_top'] = sorted(inits, key=lambda i: -i['ms'])[:8]
        # Per-process CPU and RSS for a typical client.
        cpu = [self.cli(['health'], agent['file'])[4] for _ in range(10)]
        # ru_maxrss of a child forked from this Python process includes Python's own
        # pre-exec RSS, so client RSS is not reported here.
        r['client_process'] = {'utime_ms_mean': sum(x.ru_utime for x in cpu) * 100, 'stime_ms_mean': sum(x.ru_stime for x in cpu) * 100}
        # Same endpoints over a direct keep-alive connection: server + loopback only.
        task = self.agent_task(agent)
        ctx = self.ctx(self.session)

        def direct(path, payload, tok, n=self.args.samples):
            out = []
            for _ in range(n + 3):
                t = time.perf_counter()
                self.api.raw(path, payload, tok, method='GET' if payload is None else 'POST')
                out.append((time.perf_counter() - t) * 1000)
            return stats(out[3:])
        r['direct_http_ms'] = {
            'health': direct('/v1/health', None, agent['token']),
            'task_show': direct('/v1/task/show', {**ctx, 'task_id': task['id']}, agent['token']),
            'task_list_100': direct('/v1/task/list', {**ctx, 'limit': 100}, agent['token']),
            'snapshot_changed': direct('/v1/session/snapshot', ctx, agent['token']),
            'mail_read_100': direct('/v1/mail/read', {**ctx, 'task_id': task['id']}, agent['token']),
            'mail_check_first_page': direct('/v1/mail/check', ctx, agent['token']),
        }
        r['cli_ms'] = {
            'task_show': stats(quiet(['task', 'show', '--workspace', self.workspace, '--session', self.session, '--task', task['id']], agent['file'])),
            'task_list_100': stats(quiet(['task', 'list', '--workspace', self.workspace, '--session', self.session, '--limit', 100], agent['file'])),
            'mail_read_100': stats(quiet(['mail', 'read', '--workspace', self.workspace, '--session', self.session, '--task', task['id']], agent['file'])),
            'mail_check_no_cursor_file': stats(quiet(['mail', 'check', '--workspace', self.workspace, '--session', self.session], agent['file'])),
        }
        sizes = {}
        for name, args in {'mail_check_no_cursor_file': ['mail', 'check'], 'task_list_100': ['task', 'list', '--limit', 100], 'mail_read_100': ['mail', 'read', '--task', task['id']]}.items():
            sizes[name] = len(self.cli([*args, '--workspace', self.workspace, '--session', self.session], agent['file'])[2])
        r['cli_stdout_bytes'] = sizes
        self.results['micro'] = r

    def agent_task(self, agent):
        mine = [t for t in self.tasks if agent in t['assignees'] and t['progress'] not in ('finished', 'cancelled')]
        return max(mine, key=lambda t: (len(t['assignees']) > 1, t['messages']))

    def cold_catchup(self):
        """An Agent without a cursor file catches up via real CLI calls (bounded)."""
        agent = self.busiest_agent()
        path = self.cursors / 'cold.json'
        write_private(path, {})
        calls, messages, total, t0 = 0, 0, 0.0, time.monotonic()
        rounds = 0
        while time.monotonic() - t0 < self.args.catchup_seconds:
            got, c, ms = self.check_round(agent, path)
            rounds += 1
            calls += c
            messages += got
            total += ms
            if got == 0:
                break
        assigned = sum(1 for t in self.tasks if agent in t['assignees'])
        backlog = sum(t['messages'] for t in self.tasks if agent in t['assignees'])
        self.results['cold_catchup'] = {'agent_tasks': assigned, 'backlog_messages': backlog, 'rounds': rounds, 'cli_calls': calls,
                                        'messages_read': messages, 'cli_ms_total': round(total, 1), 'finished': messages >= backlog,
                                        'cursor_file_bytes': path.stat().st_size}

    def check_round(self, agent, path, record=None, level=None, worker=None):
        """One documented mail-check round via the CLI: follow next_task, merge cursors."""
        after, calls, got, total = '', 0, 0, 0.0
        while True:
            args = ['mail', 'check', '--workspace', self.workspace, '--session', self.session, '--cursor-file', path]
            if after:
                args += ['--after-task', after]
            ms, rc, out, err, ru = self.cli(args, agent['file'])
            calls += 1
            total += ms
            if record is not None:
                record(level, worker, 'mail check', ms, rc, err, ru)
            if rc != 0:
                return got, calls, total
            data = json.loads(out)['data']
            t = time.perf_counter()
            cursors = json.loads(path.read_text())
            for page in data['tasks']:
                cursors[page['task_id']] = page['cursor']
                got += len(page['messages'])
            write_private(path, cursors)
            if record is not None:
                record(level, worker, 'cursor merge (agent side)', (time.perf_counter() - t) * 1000, 0, b'', None)
            after = data.get('next_task', '')
            if not after:
                return got, calls, total

    # ---------- concurrent load ----------
    def seed_cursors(self):
        """Agents in a long-running Session already hold cursors at the end."""
        for agent in self.agents:
            cursors, after = {}, ''
            while True:
                payload = {**self.ctx(self.session), 'cursors': cursors, 'task_limit': 100, 'limit': 500}
                if after:
                    payload['after_task'] = after
                data = self.api.call('/v1/mail/check', payload, agent['token'])
                more = False
                for page in data['tasks']:
                    cursors[page['task_id']] = page['cursor']
                    more = more or page['more']
                after = data.get('next_task', '')
                if not after and not more:
                    break
            write_private(self.cursors / f'{agent["name"]}.json', cursors)

    def browser(self, stop_event, tok, log):
        """Emulates app.js refresh(): conditional snapshot every 2 s, full parallel
        collection on change, three attempts on 409, then 4-30 s backoff."""
        etag, known, failures = '', 0, 0
        ctx = self.ctx(self.session)
        apis = [Api(self) for _ in range(4)]
        while not stop_event.is_set():
            t = time.perf_counter()
            entry = {'t': time.time(), 'requests': 0, 'bytes': 0, 'conflicts': 0, 'outcome': ''}
            try:
                for attempt in range(3):
                    headers = {'If-None-Match': etag} if etag else {}
                    status, data, resp = self.api.raw('/v1/session/snapshot', {**ctx, 'known_revision': known}, tok, headers)
                    entry['requests'] += 1
                    entry['bytes'] += len(data)
                    if status == 304:
                        entry['outcome'] = 'unchanged'
                        break
                    snap = json.loads(data)['data']
                    if snap['unchanged']:
                        entry['outcome'] = 'unchanged'
                        break
                    rev = snap['session']['read_revision']
                    results, errors = {}, []

                    def collect(kind, api, rev=rev, entry=entry, errors=errors, results=results):
                        items, after = [], ''
                        try:
                            while True:
                                st, body, _ = api.raw('/v1/session/page', {**ctx, 'kind': kind, 'read_revision': rev, 'limit': 100, 'after': after}, tok)
                                entry['requests'] += 1
                                entry['bytes'] += len(body)
                                if st != 200:
                                    errors.append(st)
                                    return
                                page = json.loads(body)['data']
                                items.extend(page['items'])
                                after = page.get('next', '')
                                if not after:
                                    results[kind] = items
                                    return
                        except Exception as e:
                            errors.append(str(e))
                    threads = [threading.Thread(target=collect, args=(k, apis[i])) for i, k in enumerate(['tasks', 'agents', 'assignments', 'dependencies'])]
                    for th in threads:
                        th.start()
                    for th in threads:
                        th.join()
                    if errors:
                        entry['conflicts'] += 1
                        if 409 in errors and attempt < 2:
                            entry['requests'] += 1  # app.js re-fetches the shell page
                            continue
                        entry['outcome'] = 'failed'
                        break
                    if any(len(results[k]) != snap['counts'][k] for k in results):
                        entry['outcome'] = 'incomplete'
                        break
                    etag, known = resp.getheader('ETag') or '', rev
                    entry['outcome'] = 'changed'
                    break
                else:
                    entry['outcome'] = 'failed'
            except Exception as e:
                entry['outcome'] = 'error:' + type(e).__name__
            entry['ms'] = (time.perf_counter() - t) * 1000
            failures = failures + 1 if entry['outcome'] in ('failed', 'incomplete') or entry['outcome'].startswith('error') else 0
            entry['next_delay_s'] = min(30, 2 * 2 ** min(failures, 4))
            log.append(entry)
            stop_event.wait(entry['next_delay_s'])

    def worker(self, level, idx, agent, stop_event, record, spec):
        spec = spec.replace(':', 'b')
        rng = random.Random(hash((spec, idx, self.run_id)))
        mine = [t for t in self.tasks if agent in t['assignees'] and t['progress'] not in ('finished', 'cancelled')]
        path = self.cursors / f'w{spec}-{idx}.json'
        shutil.copy(self.cursors / f'{agent["name"]}.json', path)
        os.chmod(path, 0o600)
        ws, se = self.workspace, self.session
        step = 0
        while not stop_event.is_set():
            step += 1
            self.check_round(agent, path, record, level, idx)
            task = rng.choice(mine)
            for attempt in range(2):
                ms, rc, out, err, ru = self.cli(['task', 'show', '--workspace', ws, '--session', se, '--task', task['id']], agent['file'])
                record(level, idx, 'task show', ms, rc, err, ru)
                if rc:
                    break
                rev = json.loads(out)['data']['revision']
                ms, rc, out, err, ru = self.cli(['task', 'status', '--workspace', ws, '--session', se, '--task', task['id'], '--progress', 'working',
                                                 '--reason', f'Load step {step}', '--expected-revision', rev, '--operation-id', f'ld-{self.run_id}-{spec}-{idx}-{step}-{attempt}'], agent['file'])
                record(level, idx, 'task status', ms, rc, err, ru)
                if rc == 0 or b'"conflict"' not in err:
                    break
            others = [a for a in task['assignees'] if a is not agent]
            body = (('@' + rng.choice(others)['name'] + ' ') if others else '') + f'Step {step} done; continuing with the next part.'
            ms, rc, out, err, ru = self.cli(['mail', 'send', '--workspace', ws, '--session', se, '--task', task['id'], '--body', body,
                                             '--operation-id', f'lm-{self.run_id}-{spec}-{idx}-{step}'], agent['file'])
            record(level, idx, 'mail send', ms, rc, err, ru)
            if self.args.think_ms:
                stop_event.wait(self.args.think_ms / 1000)

    def fetch(self, path, dest, timeout=120):
        c = http.client.HTTPConnection('127.0.0.1', urlsplit(self.pprof).port, timeout=timeout)
        c.request('GET', path)
        data = c.getresponse().read()
        Path(dest).write_bytes(data)
        c.close()

    def load(self):
        a = self.args
        view = json.loads(self.cli(['open', '--workspace', self.workspace, '--session', self.session, '--no-browser', '--json'], self.op_file, 'operator')[2])['data']['url']
        browser_token = urlsplit(view).fragment  # never printed
        calls_csv = (self.out / 'cli-calls.csv').open('w')
        calls_csv.write('level,worker,command,t,ms,rc,error,utime_ms,stime_ms\n')
        lock = threading.Lock()
        per_level = {}
        samples = []
        sampler_stop = threading.Event()
        threading.Thread(target=self.sampler, args=(sampler_stop, samples), daemon=True).start()
        for spec in a.levels:
            level, browsers = (int(x) for x in spec.split(':')) if ':' in spec else (int(spec), a.browsers)
            records = []

            def record(level, worker, cmd, ms, rc, err, ru, records=records):
                code = ''
                if rc:
                    try:
                        code = json.loads(err)['error']['code']
                    except Exception:
                        code = 'unparsed'
                row = (level, worker, cmd, time.time(), ms, rc, code, ru.ru_utime * 1000 if ru else '', ru.ru_stime * 1000 if ru else '')
                with lock:
                    records.append(row)
                    calls_csv.write(','.join(str(round(x, 3) if isinstance(x, float) else x) for x in row) + '\n')
            stop_event = threading.Event()
            blog = []
            cpu0, t0 = self.proc_sample(), time.time()
            threads = [threading.Thread(target=self.browser, args=(stop_event, browser_token, blog), daemon=True) for _ in range(browsers)]
            threads += [threading.Thread(target=self.worker, args=(level, i, self.agents[i % len(self.agents)], stop_event, record, spec), daemon=True)
                        for i in range(level)]
            for th in threads:
                th.start()
            extra = {}
            if spec == str(a.profile_level):
                secs = max(5, a.level_seconds - 15)
                prof = threading.Thread(target=self.fetch, args=(f'/debug/pprof/profile?seconds={secs}', self.out / f'cpu-L{level}.pprof', secs + 60))
                time.sleep(5)
                prof.start()
                time.sleep(max(1, a.level_seconds / 2 - 5))
                b = self.backup(f'under-load-L{level}')
                extra['backup_under_load'] = {k: v for k, v in b.items() if k != 'path'}
                extra['backup_window'] = (time.time() - b['ms'] / 1000, time.time())
                b['path'].unlink()
                remaining = a.level_seconds - (time.time() - t0)
                if remaining > 0:
                    stop_event.wait(remaining)
            else:
                stop_event.wait(a.level_seconds)
            stop_event.set()
            for th in threads:
                th.join()
            if spec == str(a.profile_level):
                prof.join()
            cpu1, t1 = self.proc_sample(), time.time()
            by_cmd = collections.defaultdict(list)
            errors = collections.Counter()
            for row in records:
                by_cmd[row[2]].append(row[4])
                if row[5]:
                    errors[(row[2], row[6])] += 1
            ok = sorted(e['t'] + e['ms'] / 1000 for e in blog if e['outcome'] in ('changed', 'unchanged'))
            marks = [t0] + ok + [t1]
            summary = {'agents': level, 'browsers': browsers, 'seconds': round(t1 - t0, 1), 'cli_calls': sum(1 for r in records if r[2] != 'cursor merge (agent side)'),
                       'calls_per_s': round(sum(1 for r in records if r[2] != 'cursor merge (agent side)') / (t1 - t0), 1),
                       'commands_ms': {k: stats(v) for k, v in by_cmd.items()}, 'errors': {f'{k[0]}:{k[1]}': v for k, v in errors.items()},
                       'service_cpu_pct': round((cpu1['cpu_s'] - cpu0['cpu_s']) / (t1 - t0) * 100, 1), 'service_rss_kb_end': cpu1['rss_kb'],
                       'client_cpu_ms_mean': round(sum(r[7] + r[8] for r in records if r[7] != '') / max(1, sum(1 for r in records if r[7] != '')), 2),
                       'browser': {'polls': len(blog), 'outcomes': dict(collections.Counter(e['outcome'] for e in blog)),
                                   'changed_ms': stats([e['ms'] for e in blog if e['outcome'] == 'changed']),
                                   'unchanged_ms': stats([e['ms'] for e in blog if e['outcome'] == 'unchanged']),
                                   'requests_per_changed': stats([e['requests'] for e in blog if e['outcome'] == 'changed']),
                                   'bytes_per_changed': stats([e['bytes'] for e in blog if e['outcome'] == 'changed']),
                                   'conflict_attempts': sum(e['conflicts'] for e in blog),
                                   'longest_gap_without_complete_model_s': round(max(b - a for a, b in zip(marks, marks[1:], strict=False)), 1) if browsers else None}, **extra}
            if 'backup_window' in extra:
                lo, hi = extra.pop('backup_window')
                overlap = [r[4] for r in records if r[2] != 'cursor merge (agent side)' and r[3] >= lo and r[3] - r[4] / 1000 <= hi]
                summary['calls_overlapping_backup_ms'] = stats(overlap)
                summary.pop('backup_window', None)
            per_level[spec] = summary
            print(f'level {spec}: {summary["calls_per_s"]} calls/s, cpu {summary["service_cpu_pct"]}%, errors {summary["errors"]}', file=sys.stderr)
            (self.out / f'browser-L{spec.replace(":", "b")}.json').write_text(json.dumps(blog))
        sampler_stop.set()
        calls_csv.close()
        with (self.out / 'service-proc.csv').open('w') as f:
            keys = list(samples[0].keys())
            f.write(','.join(keys) + '\n')
            for s in samples:
                f.write(','.join(str(s[k]) for k in keys) + '\n')
        self.fetch('/debug/pprof/heap', self.out / 'heap-end.pprof')
        self.fetch('/debug/pprof/allocs', self.out / 'allocs-end.pprof')
        if a.block_rate:
            self.fetch('/debug/pprof/block', self.out / 'block-end.pprof')
            self.fetch('/debug/pprof/mutex', self.out / 'mutex-end.pprof')
        self.results['load'] = per_level

    def idle_poll(self):
        """Browser poll costs with no writers, at final size."""
        ctx = self.ctx(self.session)
        tok = self.op_token
        changed, unchanged, reqs = [], [], 0
        for i in range(self.args.samples + 2):
            t = time.perf_counter()
            snap = self.api.call('/v1/session/snapshot', ctx, tok)
            rev, n = snap['session']['read_revision'], 1
            for kind in ['tasks', 'agents', 'assignments', 'dependencies']:
                after = ''
                while True:
                    page = self.api.call('/v1/session/page', {**ctx, 'kind': kind, 'read_revision': rev, 'limit': 100, 'after': after}, tok)
                    n += 1
                    after = page.get('next', '')
                    if not after:
                        break
            if i >= 2:
                changed.append((time.perf_counter() - t) * 1000)
            reqs = n
        tag = f'"{self.session}:{rev}"'
        for i in range(self.args.samples + 2):
            t = time.perf_counter()
            status, data, _ = self.api.raw('/v1/session/snapshot', {**ctx, 'known_revision': rev}, tok, {'If-None-Match': tag})
            check(status == 304, 'expected 304')
            if i >= 2:
                unchanged.append((time.perf_counter() - t) * 1000)
        self.results['idle_poll'] = {'changed_sequential_ms': stats(changed), 'requests_per_changed': reqs, 'unchanged_304_ms': stats(unchanged)}

    def go_bench(self, backup_path):
        copy = self.fixture / f'benchdb-{self.run_id}'
        copy.mkdir(mode=0o700)
        db = copy / 'state.db'
        shutil.copy(backup_path, db)
        os.chmod(db, 0o600)
        agent = self.busiest_agent()
        task = self.agent_task(agent)
        peer = next(a for a in task['assignees'] if a is not agent)
        fixture = self.fixture / 'bench-fixture.json'
        write_private(fixture, {'database': str(db), 'workspace_id': self.workspace, 'session_id': self.session, 'operator_token': self.op_token,
                                'agent_token': agent['token'], 'agent_name': agent['name'], 'task_id': task['id'], 'peer_name': peer['name']})
        p = subprocess.run(['go', 'test', '-tags', 'bench', '-run', '^$', '-bench', '.', '-benchmem', '-benchtime', self.args.benchtime, '-count', '1',
                            '-cpuprofile', str(self.out / 'cpu-gobench.pprof'), '-o', str(self.bin / 'gobench.test'), './internal/stack'],
                           cwd=REPO, env=dict(os.environ, ASTACK_BENCH_FIXTURE=str(fixture)), capture_output=True, text=True, timeout=1800)
        (self.out / 'gobench.txt').write_text(p.stdout + p.stderr)
        shutil.rmtree(copy, ignore_errors=True)
        if self.args.reuse:
            Path(backup_path).unlink(missing_ok=True)
        rows = {}
        for line in p.stdout.splitlines():
            if line.startswith('Benchmark'):
                parts = line.split()
                row = {'n': int(parts[1])}
                for value, unit in zip(parts[2::2], parts[3::2], strict=False):
                    row[unit] = float(value)
                rows[parts[0].rsplit('-', 1)[0]] = row
        self.results['go_bench'] = {'agent_tasks': sum(1 for t in self.tasks if agent in t['assignees']), 'task_messages': task['messages'], 'rows': rows}

    def pprof_text(self):
        tool = ['go', 'tool', 'pprof']
        jobs = {'cpu-L-top.txt': ['-top', '-nodecount=45'], 'cpu-L-cum.txt': ['-top', '-cum', '-nodecount=45'],
                'cpu-L-stack.txt': ['-top', '-cum', '-nodecount=70', '-focus=a-stack/internal/stack', '-show=a-stack/internal/stack|encoding/json|database/sql|net/http|go-sqlite3']}
        for prof in self.out.glob('cpu-L*.pprof'):
            for name, flags in jobs.items():
                r = subprocess.run(tool + flags + [str(self.bin / 'svc.test'), str(prof)], capture_output=True, text=True)
                (self.out / name.replace('-L-', f'-{prof.stem.split("-")[1]}-')).write_text(r.stdout)
        for prof, flags, name in [('allocs-end.pprof', ['-sample_index=alloc_space', '-top', '-nodecount=40'], 'allocs-top.txt'),
                                  ('heap-end.pprof', ['-sample_index=inuse_space', '-top', '-nodecount=25'], 'heap-inuse-top.txt'),
                                  ('block-end.pprof', ['-top', '-nodecount=25'], 'block-top.txt'),
                                  ('mutex-end.pprof', ['-top', '-nodecount=25'], 'mutex-top.txt')]:
            if (self.out / prof).exists():
                r = subprocess.run(tool + flags + [str(self.bin / 'svc.test'), str(self.out / prof)], capture_output=True, text=True)
                (self.out / name).write_text(r.stdout)
        if (self.out / 'cpu-gobench.pprof').exists():
            r = subprocess.run(tool + ['-top', '-nodecount=40', str(self.bin / 'gobench.test'), str(self.out / 'cpu-gobench.pprof')], capture_output=True, text=True)
            (self.out / 'cpu-gobench-top.txt').write_text(r.stdout)

    def run(self):
        a = self.args
        self.machine()
        self.build()
        if a.plans_only:
            self.load_fixture()
            after_busy = next(b['path'] for b in self.results['backups'] if b['label'] == 'after-busy')
            self.results['analysis'] = self.analyse(after_busy)
            self.engine_plans(after_busy)
            self.results['finished'] = time.strftime('%Y-%m-%dT%H:%M:%S%z')
            (self.out / 'results-plans.json').write_text(json.dumps(self.results, indent=1, default=str))
            return
        self.start()
        if a.load_only:
            try:
                self.load_fixture()
                self.seed_cursors()
                self.load()
                self.results['final_storage'] = self._storage()
            finally:
                self.stop()
            self.pprof_text()
            self.results['finished'] = time.strftime('%Y-%m-%dT%H:%M:%S%z')
            (self.out / 'results.json').write_text(json.dumps(self.results, indent=1, default=str))
            return
        try:
            if a.reuse:
                self.load_fixture()
            else:
                t = time.monotonic()
                self.build_fixture()
                self.results['fixture_seconds'] = round(time.monotonic() - t, 1)
                if a.keep:
                    self.save_fixture()
                final = self.results['backups'][-1]
                self.results['analysis'] = self.analyse(final['path'])
                self.engine_plans(final['path'])
            self.micro()
            self.idle_poll()
            self.cold_catchup()
            self.seed_cursors()
            self.load()
            self.results['backups'].append(self.backup(f'after-load-{self.run_id}'))
            self.results['final_storage'] = self._storage()
            self.results['service_stats_tail'] = (self.out / 'service-stats.jsonl').read_text().splitlines()[-1]
            final_after = self.results['backups'][-1]['path']
        finally:
            self.stop()
        self.go_bench(final_after)
        self.pprof_text()
        for b in self.results['backups']:
            b['path'] = str(b['path']).replace(str(self.fixture), '<fixture>')
        self.results['finished'] = time.strftime('%Y-%m-%dT%H:%M:%S%z')
        (self.out / 'results.json').write_text(json.dumps(self.results, indent=1, default=str))

    def cleanup(self):
        self.stop()
        if not self.args.keep and not self.args.reuse:
            shutil.rmtree(self.fixture, ignore_errors=True)


def quartiles(values):
    n = len(values)
    return [values[i * n // 4:(i + 1) * n // 4] for i in range(4)] if n >= 4 else [values]


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--out', required=True)
    ap.add_argument('--quick', action='store_true')
    ap.add_argument('--keep', action='store_true', help='keep the disposable fixture directory (and its metadata for --reuse)')
    ap.add_argument('--reuse', help='reuse a fixture directory kept by an earlier --keep run (for A/B runs); never deleted')
    ap.add_argument('--src', help='source tree to build and benchmark (default: this checkout), e.g. a patched copy for A/B')
    ap.add_argument('--plans-only', action='store_true', help='with --reuse: only write query plans for the kept fixture')
    ap.add_argument('--load-only', action='store_true', help='with --reuse: only run the concurrent CLI levels (for interleaved A/B runs)')
    ap.add_argument('--seed', type=int, default=11)
    ap.add_argument('--agents', type=int, default=20)
    ap.add_argument('--tasks', type=int, default=4000)
    ap.add_argument('--messages', type=int, default=80000)
    ap.add_argument('--closed-sessions', type=int, default=3)
    ap.add_argument('--closed-tasks', type=int, default=150)
    ap.add_argument('--closed-messages', type=int, default=1500)
    ap.add_argument('--side-tasks', type=int, default=100)
    ap.add_argument('--side-messages', type=int, default=1000)
    ap.add_argument('--writers', type=int, default=4, help='parallel HTTP fixture writers')
    ap.add_argument('--levels', default='1,4,10,20,20:0,20:3,32', help='concurrent CLI Agents per level, optionally :browsers')
    ap.add_argument('--level-seconds', type=int, default=60)
    ap.add_argument('--profile-level', type=int, default=20)
    ap.add_argument('--browsers', type=int, default=2)
    ap.add_argument('--think-ms', type=int, default=0)
    ap.add_argument('--samples', type=int, default=20)
    ap.add_argument('--catchup-seconds', type=int, default=90)
    ap.add_argument('--block-rate', type=int, default=100000, help='Go block profile rate in ns (0 = off)')
    ap.add_argument('--benchtime', default='2s')
    ap.add_argument('--max-seconds', type=int, default=5400, help='hard service lifetime')
    a = ap.parse_args()
    if a.quick:
        a.tasks, a.messages, a.closed_sessions, a.closed_tasks, a.closed_messages = 300, 3000, 1, 30, 200
        a.side_tasks, a.side_messages, a.levels, a.level_seconds, a.profile_level = 20, 100, '1,4', 20, 4
        a.samples, a.catchup_seconds, a.benchtime = 5, 20, '200ms'
    a.levels = str(a.levels).split(',')
    check(Path(a.out).is_absolute(), '--out must be absolute')
    check(not (a.plans_only or a.load_only) or a.reuse, '--plans-only and --load-only need --reuse')
    if a.src:
        global REPO
        REPO = Path(a.src).resolve()
        check((REPO / 'cmd/a-stack').is_dir(), '--src must be an a-stack source tree')
    check(max(int(x.split(':')[0]) for x in a.levels) <= 32, 'never more than 32 concurrent CLI processes')
    bench = Bench(a)

    def interrupt(*_):
        raise KeyboardInterrupt  # runs the cleanup below: stop the service, delete the fixture
    signal.signal(signal.SIGTERM, interrupt)
    try:
        bench.run()
    finally:
        bench.cleanup()
    print(json.dumps({'results': str(bench.out / 'results.json')}))


if __name__ == '__main__':
    main()
