// Tests for the Highbay companion mod. main.js runs inside Herdr, so these
// tests load it into a Node VM with recording stand-ins for the gpui-kit,
// gpui-base and herdr modules. Run with `node --test` from this folder.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const source = (await readFile(new URL('./main.js', import.meta.url), 'utf8'))
  .replace(/^import .*;\n/gm, '')
  .replace(/^export default class /m, 'class ')
  .replace(/^export function /gm, 'function ')
  + '\nglobalThis.mod = { projectFolder, parseStatus, roleTerminal, stageLine, roleLine, HighbayCompanion };';

// element records one built element: its id, text children, nested elements
// and the handlers main.js attached.
function element() {
  const node = { id: null, children: [], handlers: {} };
  const proxy = new Proxy(node, {
    get(target, name) {
      if (name === 'node') return target;
      if (name === 'child') return child => { target.children.push(child); return proxy; };
      if (name === 'id') return id => { target.id = id; return proxy; };
      if (name === 'when') return (cond, build) => (cond ? build(proxy) : proxy);
      if (name === 'on_click') return handler => { target.handlers.click = handler; return proxy; };
      if (name === 'on_mouse_down') return (button, handler) => { target.handlers[`down:${button}`] = handler; return proxy; };
      return () => proxy;
    },
  });
  return proxy;
}

// walk lists every element node under root, root included.
function walk(root) {
  const node = root.node;
  return [node, ...node.children.filter(c => typeof c === 'object' && c?.node).flatMap(walk)];
}

// texts lists every string shown under root, in order.
function texts(root) {
  return walk(root).flatMap(node => node.children.filter(c => typeof c === 'string'));
}

// plain copies a value made inside the VM into this realm, so deepEqual
// compares contents rather than the VM's own prototypes.
function plain(value) {
  return JSON.parse(JSON.stringify(value));
}

// load starts a fresh copy of main.js against a fake Herdr in state, and
// returns the module plus the calls it made.
function load(state) {
  const modSource = state.executable
    ? source.replace('const HBAY_COMMAND = "hbay";', `const HBAY_COMMAND = ${JSON.stringify(state.executable)};`)
    : source;
  const calls = { exec: [], run: [], closed: [] };
  const herdr = {
    spaces: () => state.spaces ?? [],
    terminals: () => state.terminals ?? [],
    panes: () => state.panes ?? [],
    focused: () => state.focused ?? null,
    theme: () => new Proxy({}, { get: (_t, name) => String(name) }),
    props: () => state.props ?? {},
    checking: () => Boolean(state.checking),
    exec: async (command, args, options) => {
      calls.exec.push(plain({ command, args, options }));
      if (state.deferred) return new Promise((resolve, reject) => state.deferred.push({ resolve, reject }));
      const commandAnswer = state.byCommand?.[command]?.[args.join(' ')];
      if (commandAnswer instanceof Error) throw commandAnswer;
      if (commandAnswer) return commandAnswer;
      const answer = state.answers?.[args.join(' ')];
      if (answer instanceof Error) throw answer;
      return answer ?? { code: 0, stdout: '{"running":false}', stderr: '' };
    },
    run: (name, args) => { calls.run.push(plain({ name, args })); return null; },
    closeOverlay: id => { calls.closed.push(id); return null; },
  };
  const context = vm.createContext({
    ...herdr,
    View: class {},
    div: element,
    h_flex: element,
    v_flex: element,
    JSON,
    String,
    Array,
    Set,
    Boolean,
  });
  vm.runInContext(modSource, context);
  return { mod: context.mod, calls };
}

// fakeCx is a GPUI context that runs spawned tasks and records timers.
function fakeCx() {
  const cx = {
    tasks: [],
    timers: [],
    notified: 0,
    stopped: 0,
    notify() { cx.notified++; },
    spawn(body) { const task = Promise.resolve(body(cx)); cx.tasks.push(task); return task; },
    stop_propagation() { cx.stopped++; },
    timer: { every(ms, handler) { cx.timers.push({ ms, handler }); } },
    async settle() { while (cx.tasks.length) await cx.tasks.shift(); },
  };
  return cx;
}

const space = { id: 'sp1', active: true, machine: null, folder: '/work/space', tabs: [{ id: 'tb1' }] };

const running = {
  running: true,
  project_name: 'homes',
  directory: '/work/homes',
  team: 'homes-1',
  herdr_mode: 'attached',
  stage: 'build',
  change_id: 'add-listings',
  change_title: 'Add listings',
  attention: 'The builder needs you.',
  roles: [
    { role: 'guide', handle: 'guide-1', state: 'working', attention: 'none', watcher: 'held', assignment: 'no_work' },
    { role: 'builder', handle: 'builder-1', state: 'needs_you', attention: 'permission', watcher: 'held', assignment: 'nothing_owed' },
    { role: 'test_writer', handle: 'tests-1', state: 'not_started', attention: 'none', watcher: 'unknown', assignment: 'unread' },
  ],
};

test('projectFolder follows labels, then focus, then panes, then the space folder', () => {
  const { mod } = load({});
  const labeled = { state: 'live', labels: { 'herdr.space': 'sp1', 'chorus.project': '/work/homes' } };
  const other = { state: 'live', labels: { 'herdr.space': 'sp2', 'chorus.project': '/work/other' } };
  const ended = { state: 'exited', labels: { 'herdr.space': 'sp1', 'chorus.project': '/work/old' } };
  const panes = [{ tab: 'tb9', cwd: '/elsewhere' }, { tab: 'tb1', cwd: '/work/pane' }];
  const cases = [
    { name: 'no space', space: null, want: null },
    { name: 'remote space', space: { ...space, machine: 'box' }, terms: [labeled], want: null },
    { name: 'labeled terminal', space, terms: [other, ended, labeled], panes, want: '/work/homes' },
    { name: 'focused pane', space, terms: [other], panes, focus: { space: 'sp1', cwd: '/work/focus' }, want: '/work/focus' },
    { name: 'focus in another space', space, panes, focus: { space: 'sp2', cwd: '/work/focus' }, want: '/work/pane' },
    { name: 'first pane of the space', space, panes, want: '/work/pane' },
    {
      name: 'focused pane of the active tab',
      space: { ...space, tabs: [{ id: 'tb1' }, { id: 'tb2', active: true }] },
      panes: [...panes, { tab: 'tb2', cwd: '/work/second' }, { tab: 'tb2', cwd: '/work/focused', focused: true }],
      want: '/work/focused',
    },
    { name: 'space folder', space, panes: [{ tab: 'tb9', cwd: '/elsewhere' }], want: '/work/space' },
    { name: 'home space', space: { ...space, folder: '' }, want: null },
  ];
  for (const c of cases) {
    assert.equal(mod.projectFolder(c.space, c.terms, c.panes, c.focus), c.want, c.name);
  }
});

test('parseStatus reads running, stopped and failed answers', () => {
  const { mod } = load({});
  const cases = [
    { name: 'stopped', result: { code: 0, stdout: '{"running":false}', stderr: '' }, kind: 'stopped' },
    { name: 'running', result: { code: 0, stdout: JSON.stringify(running), stderr: '' }, kind: 'running' },
    { name: 'failed', result: { code: 1, stdout: '', stderr: 'hbay: boom\n' }, kind: 'error', message: 'hbay: boom' },
    { name: 'failed silently', result: { code: 2, stdout: '', stderr: '' }, kind: 'error', message: 'hbay status failed.' },
    { name: 'not JSON', result: { code: 0, stdout: 'hello', stderr: '' }, kind: 'error' },
    { name: 'no running field', result: { code: 0, stdout: '{}', stderr: '' }, kind: 'error' },
    { name: 'null', result: { code: 0, stdout: 'null', stderr: '' }, kind: 'error' },
    { name: 'missing field', result: { code: 0, stdout: JSON.stringify({ ...running, stage: undefined }), stderr: '' }, kind: 'error', message: 'hbay status answered without stage.' },
    { name: 'wrong type', result: { code: 0, stdout: JSON.stringify({ ...running, change_id: null }), stderr: '' }, kind: 'error', message: 'hbay status answered without change_id.' },
    { name: 'no roles', result: { code: 0, stdout: JSON.stringify({ ...running, roles: {} }), stderr: '' }, kind: 'error', message: 'hbay status answered without roles.' },
    { name: 'incomplete role', result: { code: 0, stdout: JSON.stringify({ ...running, roles: [{ role: 'guide' }] }), stderr: '' }, kind: 'error', message: 'hbay status answered a role without handle.' },
  ];
  for (const c of cases) {
    const got = mod.parseStatus(c.result);
    assert.equal(got.kind, c.kind, c.name);
    if (c.message) assert.equal(got.message, c.message, c.name);
  }
  const got = mod.parseStatus({ code: 0, stdout: JSON.stringify(running), stderr: '' });
  assert.equal(got.status.projectName, 'homes');
  assert.equal(got.status.directory, '/work/homes');
  assert.equal(got.status.herdrMode, 'attached');
  assert.equal(got.status.roles.length, 3);
});

test('roleTerminal finds the live terminal labeled with the project and handle', () => {
  const { mod } = load({});
  const terms = [
    { address: 't1', state: 'exited', labels: { 'chorus.project': '/work/homes', 'chorus.handle': 'builder-1' } },
    { address: 't2', state: 'live', labels: { 'chorus.project': '/work/other', 'chorus.handle': 'builder-1' } },
    { address: 't3', state: 'live', labels: { 'chorus.project': '/work/homes', 'chorus.handle': 'builder-1' } },
  ];
  assert.equal(mod.roleTerminal(terms, '/work/homes', 'builder-1'), 't3');
  assert.equal(mod.roleTerminal(terms, '/work/homes', 'tests-1'), null);
  assert.equal(mod.roleTerminal(terms, '', 'builder-1'), null);
});

test('stageLine and roleLine describe the project for people', () => {
  const { mod } = load({});
  assert.equal(mod.stageLine({ stage: 'build', changeTitle: 'Add listings' }), 'Build · Add listings');
  assert.equal(mod.stageLine({ stage: 'describe', changeTitle: '' }), 'Describe');
  assert.equal(mod.stageLine({ stage: 'later', changeTitle: '' }), 'Unknown stage');
  assert.equal(mod.roleLine(running.roles[0]), 'Coding agent: working');
  assert.equal(mod.roleLine({ role: 'guide', state: 'unknown', attention: 'none' }), 'Coding agent: status unknown');
  assert.equal(mod.roleLine({ role: 'guide', state: 'unknown', attention: 'none' }, 'attached'), 'Coding agent: status unknown');
  assert.equal(mod.roleLine({ role: 'guide', state: 'unknown', attention: 'none' }, 'available'), 'Coding agent: status unknown');
  assert.equal(mod.roleLine({ role: 'guide', state: 'unknown', attention: 'none' }, 'unavailable'), 'Coding agent: status unknown');
  assert.equal(mod.roleLine({ role: 'builder', state: 'unknown', attention: 'none' }, 'available'), 'Builder: status unknown');
  assert.equal(mod.roleLine(running.roles[1]), 'Builder: needs you (permission)');
  assert.equal(mod.roleLine(running.roles[2]), 'Test writer: not started');
});

test('init starts no command while Herdr checks the mod', () => {
  const { mod, calls } = load({ checking: true, spaces: [space] });
  const view = new mod.HighbayCompanion();
  const cx = fakeCx();
  view.init(undefined, cx);
  assert.equal(calls.exec.length, 0);
  assert.equal(cx.timers.length, 0);
  assert.deepEqual(texts(view.render(cx)), ['Highbay', 'Reading the project…']);
});

test('init reads the status in the project folder and polls every four seconds', async () => {
  const { mod, calls } = load({ spaces: [space] });
  const view = new mod.HighbayCompanion();
  const cx = fakeCx();
  view.init(undefined, cx);
  await cx.settle();
  assert.deepEqual(calls.exec, [{ command: 'hbay', args: ['status', '--json'], options: { cwd: '/work/space', timeout: 10000 } }]);
  assert.deepEqual(cx.timers.map(t => t.ms), [4000, 500]);
  cx.timers[0].handler(cx);
  await cx.settle();
  assert.equal(calls.exec.length, 2);
  assert.ok(texts(view.render(cx)).includes('No Highbay server is running for this folder.'));
});

test('a remote space reads nothing and offers nothing', async () => {
  const { mod, calls } = load({ spaces: [{ ...space, machine: 'box' }] });
  const view = new mod.HighbayCompanion();
  const cx = fakeCx();
  view.init(undefined, cx);
  await cx.settle();
  assert.equal(calls.exec.length, 0);
  const root = view.render(cx);
  assert.ok(texts(root).includes('Show a space on this computer to see its Highbay project.'));
  assert.ok(!walk(root).some(node => node.id === 'highbay-open'));
});

test('a running project shows its stage, roles and pane shortcuts', async () => {
  const terminals = [{ address: 't7', state: 'live', labels: { 'herdr.space': 'sp1', 'chorus.project': '/work/homes', 'chorus.handle': 'builder-1' } }];
  const { mod, calls } = load({
    spaces: [space],
    terminals,
    props: { overlay: 'ov1' },
    answers: { 'status --json': { code: 0, stdout: JSON.stringify(running), stderr: '' } },
  });
  const view = new mod.HighbayCompanion();
  const cx = fakeCx();
  view.init(undefined, cx);
  await cx.settle();
  assert.equal(calls.exec[0].options.cwd, '/work/homes');
  const root = view.render(cx);
  const shown = texts(root);
  for (const text of ['Highbay · homes', 'Build · Add listings', 'The builder needs you.', 'Coding agent: working', 'Builder: needs you (permission)', 'Test writer: not started', 'Open Highbay Window']) {
    assert.ok(shown.includes(text), text);
  }
  const ids = walk(root).map(node => node.id).filter(Boolean);
  assert.deepEqual(ids.filter(id => id.startsWith('highbay-show-')), ['highbay-show-builder']);

  // Every button stops the press so it does not drag the window.
  const buttons = walk(root).filter(node => node.handlers.click);
  assert.deepEqual(buttons.map(node => node.id), ['highbay-close', 'highbay-show-builder', 'highbay-open']);
  for (const button of buttons) {
    const before = cx.stopped;
    button.handlers['down:left']({}, cx);
    assert.equal(cx.stopped, before + 1, button.id);
  }

  buttons[1].handlers.click({}, cx);
  assert.deepEqual(calls.run, [{ name: 'pane.focus', args: { terminal: 't7' } }]);
  buttons[0].handlers.click({}, cx);
  assert.deepEqual(calls.closed, ['ov1']);
});

test('Open Highbay Window shows Opening, then Opened in browser, and clears on next poll', async () => {
  const state = { spaces: [space], deferred: [] };
  const { mod } = load(state);
  const view = new mod.HighbayCompanion();
  const cx = fakeCx();
  view.init(undefined, cx);
  state.deferred.shift().resolve({ code: 0, stdout: JSON.stringify({ ...running, herdr_mode: 'available', roles: [{ role: 'guide', handle: 'guide-1', state: 'unknown', attention: 'none', watcher: 'unknown', assignment: 'no_work' }] }), stderr: '' });
  await tick();
  assert.ok(texts(view.render(cx)).includes('Coding agent: status unknown'));
  assert.ok(texts(view.render(cx)).includes('Open Highbay Window'));

  walk(view.render(cx)).find(node => node.id === 'highbay-open').handlers.click({}, cx);
  assert.ok(texts(view.render(cx)).includes('Opening…'));

  state.deferred.shift().resolve({ code: 0, stdout: '', stderr: '' });
  await tick();
  state.deferred.shift().resolve({ code: 0, stdout: JSON.stringify(running), stderr: '' });
  await tick();
  assert.ok(texts(view.render(cx)).includes('Opened in browser'));

  cx.timers[0].handler(cx);
  state.deferred.shift().resolve({ code: 0, stdout: JSON.stringify(running), stderr: '' });
  await tick();
  assert.ok(!texts(view.render(cx)).includes('Opened in browser'));
});

test('Start Highbay runs hbay in the project folder and reports a failure', async () => {
  const { mod, calls } = load({
    spaces: [space],
    answers: { '': { code: 1, stdout: '', stderr: 'hbay: no project here\n' } },
  });
  const view = new mod.HighbayCompanion();
  const cx = fakeCx();
  view.init(undefined, cx);
  await cx.settle();
  let open = walk(view.render(cx)).find(node => node.id === 'highbay-open');
  assert.ok(texts({ node: open }).includes('Start Highbay'));
  open.handlers.click({}, cx);
  open.handlers.click({}, cx);
  await cx.settle();
  const opens = calls.exec.filter(call => call.args.length === 0);
  assert.deepEqual(opens, [{ command: 'hbay', args: [], options: { cwd: '/work/space', timeout: 60000 } }]);
  assert.ok(texts(view.render(cx)).includes('hbay: no project here'));
});

test('a command that cannot start is shown, not thrown', async () => {
  const { mod } = load({ spaces: [space], answers: { 'status --json': new Error('not installed') } });
  const view = new mod.HighbayCompanion();
  const cx = fakeCx();
  view.init(undefined, cx);
  await cx.settle();
  assert.ok(texts(view.render(cx)).includes('Could not run hbay: not installed'));
});

// tick lets every promise that can settle now do so.
async function tick() {
  for (let i = 0; i < 5; i++) await new Promise(resolve => setImmediate(resolve));
}

test('a space switch during a status read reads the new folder and drops the old answer', async () => {
  const payments = { id: 'sp2', active: false, machine: null, folder: '/work/payments', tabs: [{ id: 'tb2' }] };
  const state = { spaces: [space, payments], deferred: [] };
  const { mod, calls } = load(state);
  const view = new mod.HighbayCompanion();
  const cx = fakeCx();
  view.init(undefined, cx);
  assert.deepEqual(calls.exec.map(call => call.options.cwd), ['/work/space']);

  state.spaces = [{ ...space, active: false }, { ...payments, active: true }];
  assert.ok(texts(view.render(cx)).includes('Reading the project…'), 'the old space is not shown');
  assert.ok(!walk(view.render(cx)).some(node => node.id === 'highbay-open'));
  cx.timers[1].handler(cx);
  assert.equal(calls.exec.length, 1, 'one read at a time');

  state.deferred.shift().resolve({ code: 0, stdout: JSON.stringify(running), stderr: '' });
  await tick();
  assert.deepEqual(calls.exec.map(call => call.options.cwd), ['/work/space', '/work/payments']);
  assert.ok(!texts(view.render(cx)).includes('Highbay · homes'), 'the old answer is dropped');

  state.deferred.shift().resolve({ code: 0, stdout: '{"running":false}', stderr: '' });
  await tick();
  assert.ok(texts(view.render(cx)).includes('No Highbay server is running for this folder.'));

  state.spaces = [{ ...space, active: false }, { ...payments, active: true, machine: 'box' }];
  const remote = view.render(cx);
  assert.ok(texts(remote).includes('Show a space on this computer to see its Highbay project.'));
  assert.ok(!walk(remote).some(node => node.id === 'highbay-open'));
  cx.timers[1].handler(cx);
  assert.equal(calls.exec.length, 2, 'a remote space runs nothing');
});

test('a Start Highbay failure stays with the folder it was for', async () => {
  const payments = { id: 'sp2', active: false, machine: null, folder: '/work/payments', tabs: [{ id: 'tb2' }] };
  const state = { spaces: [space, payments], deferred: [] };
  const { mod, calls } = load(state);
  const view = new mod.HighbayCompanion();
  const cx = fakeCx();
  view.init(undefined, cx);
  state.deferred.shift().resolve({ code: 0, stdout: '{"running":false}', stderr: '' });
  await tick();

  walk(view.render(cx)).find(node => node.id === 'highbay-open').handlers.click({}, cx);
  assert.deepEqual(calls.exec.at(-1), { command: 'hbay', args: [], options: { cwd: '/work/space', timeout: 60000 } });
  state.spaces = [{ ...space, active: false }, { ...payments, active: true }];
  state.deferred.shift().reject(new Error('not installed'));
  await tick();
  assert.ok(!texts(view.render(cx)).includes('Could not run hbay: not installed'));

  state.spaces = [space, payments];
  while (state.deferred.length) state.deferred.shift().resolve({ code: 0, stdout: '{"running":false}', stderr: '' });
  await tick();
  assert.ok(texts(view.render(cx)).includes('Could not run hbay: not installed'));
});

test('uses the setup-configured hbay executable path without retrying bare hbay on failure', async () => {
  const configured = '/opt/go/bin/hbay';
  const { mod, calls } = load({
    executable: configured,
    spaces: [space],
    byCommand: {
      [configured]: {
        'status --json': { code: 0, stdout: JSON.stringify(running), stderr: '' },
        '': new Error('timed out after 60000ms'),
      },
      hbay: {
        '': new Error('No such file or directory (os error 2)'),
      },
    },
  });
  const view = new mod.HighbayCompanion();
  const cx = fakeCx();
  view.init(undefined, cx);
  await cx.settle();
  assert.equal(calls.exec[0].command, configured);
  assert.ok(texts(view.render(cx)).includes('Highbay · homes'));

  walk(view.render(cx)).find(node => node.id === 'highbay-open').handlers.click({}, cx);
  await cx.settle();
  assert.deepEqual(
    calls.exec.slice(1).map(call => [call.command, call.args]),
    [
      [configured, []],
      [configured, ['status', '--json']],
    ],
  );
  assert.ok(texts(view.render(cx)).includes('Could not run hbay: timed out after 60000ms'));
});
