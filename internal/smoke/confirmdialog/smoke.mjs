// CDP smoke of the console confirm dialog (P3-3b, CERB-GAP-886). Drives
// headless Chrome against the helper's console on 127.0.0.1:4799, whose
// scratch HOME is SMOKE_HOME. Run it with run.sh, not by hand.
import { spawn } from 'node:child_process'
import fs from 'node:fs'

const LOGIN = await (await fetch('http://127.0.0.1:4798/login-url')).text()
const HOME = process.env.SMOKE_HOME
if (!HOME || !HOME.startsWith('/tmp/')) { console.error('SMOKE_HOME must be a scratch directory under /tmp'); process.exit(2) }
const results = []
const check = (name, ok, detail = '') => { results.push({ name, ok, detail }); console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ' — ' + detail : ''}`) }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const chrome = spawn(process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', [
  '--headless=new', '--remote-debugging-port=9333', `--user-data-dir=${HOME}/chrome`, '--no-first-run', '--no-default-browser-check', 'about:blank',
], { stdio: 'ignore' })
process.on('exit', () => chrome.kill())

let targets
for (let i = 0; i < 50; i++) {
  try { targets = await (await fetch('http://127.0.0.1:9333/json/list')).json(); if (targets.some((t) => t.type === 'page')) break } catch {}
  await sleep(200)
}
const page = targets.find((t) => t.type === 'page')
const ws = new WebSocket(page.webSocketDebuggerUrl)
await new Promise((r) => ws.addEventListener('open', r))
let seq = 0
const pending = new Map()
ws.addEventListener('message', (e) => { const m = JSON.parse(e.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id) } })
const send = (method, params = {}) => new Promise((r) => { const id = ++seq; pending.set(id, r); ws.send(JSON.stringify({ id, method, params })) })
const evaluate = async (expr) => {
  const m = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true })
  if (m.result?.exceptionDetails) throw new Error(m.result.exceptionDetails.exception?.description || 'eval failed')
  return m.result?.result?.value
}
const waitFor = async (expr, what, ms = 8000) => {
  const end = Date.now() + ms
  while (Date.now() < end) { if (await evaluate(expr)) return true; await sleep(100) }
  throw new Error('timed out waiting for ' + what)
}
await send('Page.enable'); await send('Runtime.enable')
const go = async (url) => { await send('Page.navigate', { url }); await sleep(900); await evaluate(lib) }

// DOM helpers, evaluated in the page.
const lib = `
window.__dialogs = () => [...document.querySelectorAll('[role=dialog],[role=alertdialog]')];
window.__top = () => { const d = __dialogs(); return d[d.length-1] };
window.__btn = (scope, text) => [...(scope||document).querySelectorAll('button')].find(b => b.textContent.trim() === text);
window.__type = (el, v) => { const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set; set.call(el, v); el.dispatchEvent(new Event('input', { bubbles: true })) };
true`

async function openConfirm(startFlow, targetName, label, verb) {
  await startFlow()
  // The ack step (ActionConfirm): the dialog that says it runs only with
  // your acknowledgment; its own verb button sends it.
  const ack = `__dialogs().find(d => /only with your acknowledgment/.test(d.textContent))`
  await waitFor(`!!${ack}`, `${label}: ack dialog`)
  await evaluate(`(__btn(${ack}, '${verb}').click(), true)`)
  await waitFor(`!!document.querySelector('[data-testid=plan-hash]')`, `${label}: plan dialog`)
  const view = await evaluate(`(() => { const t = __top(); const bolds = [...t.querySelectorAll('.font-semibold')].map(e => e.textContent); return { text: t.textContent, bolds, hash: document.querySelector('[data-testid=plan-hash]').textContent.trim(), disabled: __btn(t, 'Confirm and run')?.disabled } })()`)
  check(`${label}: the plan is shown with the full hash`, /^sha256:[0-9a-f]{64}$/.test(view.hash), view.hash)
  check(`${label}: effect, target with labels and computed_by in bold`, view.bolds.some((b) => b.startsWith('Effect:')) && view.bolds.some((b) => b.includes('env dev, owner self, admin self')) && view.bolds.some((b) => b.startsWith('Computed by: socket')), view.bolds.join(' | '))
  check(`${label}: confirm is disabled before the target is typed`, view.disabled === true)
  await evaluate(`(__type(document.querySelector('[data-testid=confirm-typed]'), 'y'), true)`)
  await sleep(150)
  let disabled = await evaluate(`__btn(__top(), 'Confirm and run').disabled`)
  await evaluate(`(__type(document.querySelector('[data-testid=confirm-typed]'), '${targetName}x'), document.querySelector('[data-testid=confirm-typed]').form?.requestSubmit(), true)`)
  await sleep(400)
  const stillOpen = await evaluate(`!!document.querySelector('[data-testid=plan-hash]')`)
  check(`${label}: a wrong target ("y", then a near miss) is refused`, disabled === true && stillOpen === true)
  return view.hash
}

async function typeAndSubmit(name) {
  await evaluate(`(__type(document.querySelector('[data-testid=confirm-typed]'), '${name}'), true)`)
  await sleep(150)
  await evaluate(`(__btn(__top(), 'Confirm and run').click(), true)`)
}

try {
  await go(LOGIN)
  await go('http://localhost:4799/resources')
  const startStop = async () => {
    await waitFor(`[...document.querySelectorAll('tr')].some(r => r.textContent.includes('web'))`, 'resource row')
    await evaluate(`[...document.querySelectorAll('tr')].find(r => r.textContent.includes('web') && r.querySelector('td')).querySelector('td').click(), true`)
    await waitFor(`!!__btn(__top(), 'Stop')`, 'detail Stop')
    await evaluate(`(__btn(__top(), 'Stop').click(), true)`)
  }
  // 1. Resource stop: stale first.
  await openConfirm(startStop, 'web', 'resource stop', 'Stop')
  const cfg = `${HOME}/.cerberus/config.yaml`
  fs.writeFileSync(cfg, fs.readFileSync(cfg, 'utf8').replace('"300"', '"301"'))
  await typeAndSubmit('web')
  await waitFor(`/changed after you were shown it/.test(document.body.textContent)`, 'stale reason')
  check('resource stop: a stale plan keeps the dialog open with the reason', await evaluate(`!!document.querySelector('[data-testid=plan-hash]')`))
  await send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 })
  await waitFor(`/Not confirmed; nothing ran/.test(document.body.textContent)`, 'cancel message')
  check('resource stop: closing the dialog reports nothing ran', true)
  // 2. Resource stop: a fresh attempt with the right target runs.
  await go('http://localhost:4799/resources'); await evaluate(lib)
  await openConfirm(startStop, 'web', 'resource stop (retry)', 'Stop')
  await typeAndSubmit('web')
  await waitFor(`!document.querySelector('[data-testid=plan-hash]')`, 'plan dialog to close')
  await sleep(500)
  const errText = await evaluate(`[...document.querySelectorAll('[class*=danger]')].map(e => e.textContent).join(' ')`)
  const stops = await evaluate(`fetch('/api/approvals').then(r => r.json()).then(j => (j.approvals || []).filter(a => a.operation === 'stop'))`)
  const consumed = stops.filter((a) => a.status === 'consumed')
  check('resource stop: the right target runs, under one consumed approval decided by the console session',
    consumed.length === 1 && consumed[0].decision?.surface === 'tty_confirm' && consumed[0].decision?.by?.via === 'web' && !!consumed[0].decision?.by?.session && !/plan_stale/.test(errText),
    JSON.stringify(stops.map((a) => [a.status, a.decision?.surface, a.decision?.by?.via])))

  // 3. Deploy profile run.
  await go('http://localhost:4799/deployments'); await evaluate(lib)
  const startRun = async () => {
    await waitFor(`!!__btn(document, 'Run')`, 'profile Run')
    await evaluate(`(__btn(document, 'Run').click(), true)`)
  }
  await openConfirm(startRun, 'site', 'deploy profile', 'Run')
  const infraPath = `${HOME}/.cerberus/infra.yaml`
  fs.writeFileSync(infraPath, fs.readFileSync(infraPath, 'utf8').replace('echo deployed', 'echo deployed-again'))
  await typeAndSubmit('site')
  await waitFor(`/changed after you were shown it/.test(document.body.textContent)`, 'deploy stale reason')
  check('deploy profile: a stale plan keeps the dialog open with the reason', await evaluate(`!!document.querySelector('[data-testid=plan-hash]')`))
  check('deploy profile: nothing ran on the stale plan', !fs.existsSync(`${HOME}/deployed.txt`))
  await send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 })
  await sleep(500)
  await go('http://localhost:4799/deployments'); await evaluate(lib)
  await openConfirm(startRun, 'site', 'deploy profile (retry)', 'Run')
  await typeAndSubmit('site')
  await waitFor(`!document.querySelector('[data-testid=plan-hash]')`, 'deploy plan dialog to close', 15000)
  for (let i = 0; i < 30 && !fs.existsSync(`${HOME}/deployed.txt`); i++) await sleep(200)
  check('deploy profile: the right target runs the profile', fs.existsSync(`${HOME}/deployed.txt`) && fs.readFileSync(`${HOME}/deployed.txt`, 'utf8').includes('deployed-again'))
  const runs = await evaluate(`fetch('/api/approvals').then(r => r.json()).then(j => (j.approvals || []).filter(a => a.operation === 'run_profile'))`)
  check('deploy profile: the run was approved in the daemon, by the console session', runs.some((a) => a.status === 'consumed' && a.decision?.by?.via === 'web'),
    JSON.stringify(runs.map((a) => [a.status, a.decision?.by?.via])))

  // 4. Break glass on a protected target (P3-5b): the CLI asks, the
  //    console shows it as BREAK GLASS and approves it with a passkey, the
  //    CLI's retry runs it, and it stays a follow-up.
  await send('WebAuthn.enable', { enableUI: false })
  await send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } })
  const enrollToken = await (await fetch('http://127.0.0.1:4798/enroll-token')).text()
  await go(`http://localhost:4799/approvals?enroll=${encodeURIComponent(enrollToken)}&label=smoke`)
  await waitFor(`!!document.querySelector('[data-testid=enroll]')`, 'enroll button')
  await evaluate(`(document.querySelector('[data-testid=enroll]').click(), true)`)
  await waitFor(`!!document.querySelector('[data-testid^=passkey-]')`, 'enrolled passkey', 15000)
  check('break glass: a passkey is enrolled on the console', true)

  const asked = await (await fetch('http://127.0.0.1:4798/break-glass')).json()
  check('break glass: on a protected target the CLI is told to finish it with a passkey', !!asked.approval_id && /BREAK GLASS/.test(asked.error || ''), (asked.error || '').slice(0, 120))
  await go(`http://localhost:4799/approvals?id=${asked.approval_id}`)
  await waitFor(`!!document.querySelector('[data-testid=break-glass-banner]')`, 'break-glass banner')
  const detail = await evaluate(`(() => { const d = document.querySelector('[data-testid=approval-detail]'); const row = document.querySelector('[data-testid="approval-${asked.approval_id}"]'); return { banner: document.querySelector('[data-testid=break-glass-banner]').textContent, text: d.textContent, row: row ? row.textContent : '', approveDisabled: __btn(d, 'Approve with passkey')?.disabled } })()`)
  check('break glass: the console labels it BREAK GLASS with the reason', /BREAK GLASS/.test(detail.banner) && detail.banner.includes('prod is down') && /BREAK GLASS/.test(detail.row), detail.banner.slice(0, 120))
  check('break glass: the plan is shown with it', /sha256:[0-9a-f]{64}/.test(detail.text))
  check('break glass: approve is disabled until the target is typed', detail.approveDisabled === true)
  await evaluate(`(__type(document.querySelector('[data-testid=typed]'), 'prod'), true)`)
  await sleep(150)
  check('break glass: a wrong target keeps approve disabled', await evaluate(`__btn(document.querySelector('[data-testid=approval-detail]'), 'Approve with passkey').disabled`))
  await evaluate(`(__type(document.querySelector('[data-testid=typed]'), 'prod-api'), true)`)
  await sleep(150)
  await evaluate(`(__btn(document.querySelector('[data-testid=approval-detail]'), 'Approve with passkey').click(), true)`)
  await waitFor(`fetch('/api/approvals').then(r => r.json()).then(j => (j.approvals || []).some(a => a.id === '${asked.approval_id}' && a.status === 'approved'))`, 'passkey approval', 15000)
  check('break glass: approved on the console with the passkey', true)
  const ran = await (await fetch(`http://127.0.0.1:4798/break-glass?id=${asked.approval_id}`)).json()
  check('break glass: the CLI retry runs under it', !ran.error && ran.result?.success !== false, ran.error || '')
  const open = await (await fetch('http://127.0.0.1:4798/follow-ups')).json()
  check('break glass: it stays a follow-up until acknowledged', open.length === 1 && open[0].id === asked.approval_id)
  await go('http://localhost:4799/')
  await waitFor(`!!document.querySelector('[data-testid=break-glass-alert]')`, 'header badge')
  check('break glass: the console header shows the badge', /BREAK GLASS · 1 today · 1 to acknowledge/.test(await evaluate(`document.querySelector('[data-testid=break-glass-alert]').textContent`)))

  // 5. Lockdown (§12): one click and no phrase to engage, a red banner, a
  //    refused operation, and a lift approved with the passkey.
  await go('http://localhost:4799/')
  await waitFor(`!!document.querySelector('[data-testid=engage-lockdown]')`, 'lockdown button')
  await evaluate(`(document.querySelector('[data-testid=engage-lockdown]').click(), true)`)
  await waitFor(`!!document.querySelector('[data-testid=lockdown-reason]')`, 'lockdown dialog')
  await evaluate(`(__type(document.querySelector('[data-testid=lockdown-reason]'), 'smoke drill'), true)`)
  await sleep(100)
  await evaluate(`(__btn(__top(), 'Lockdown').click(), true)`)
  await sleep(900); await evaluate(lib)
  await waitFor(`!!document.querySelector('[data-testid=brakes-banner]')`, 'lockdown banner')
  const banner = await evaluate(`document.querySelector('[data-testid=brakes-banner]').textContent`)
  check('lockdown: engaged from the console with no typed phrase, and the banner says so', /LOCKDOWN/.test(banner) && banner.includes('smoke drill'), banner.slice(0, 120))
  const refused = await (await fetch('http://127.0.0.1:4798/stop')).json()
  check('lockdown: a stop is refused, naming how to lift it', /lockdown/.test(refused.error || '') && /cerberus lockdown --off/.test(refused.error || ''), (refused.error || '').slice(0, 160))
  await evaluate(`(document.querySelector('[data-testid=lift-lockdown]').click(), true)`)
  await waitFor(`location.pathname === '/approvals' && /id=apr_/.test(location.search)`, 'lift approval page', 10000)
  await sleep(600); await evaluate(lib)
  const liftID = await evaluate(`new URLSearchParams(location.search).get('id')`)
  await waitFor(`!!document.querySelector('[data-testid=approval-detail]')`, 'lift approval detail')
  await evaluate(`(__type(document.querySelector('[data-testid=typed]'), 'brake.lockdown'), true)`)
  await sleep(150)
  await evaluate(`(__btn(document.querySelector('[data-testid=approval-detail]'), 'Approve with passkey').click(), true)`)
  await waitFor(`!!document.querySelector('[data-testid=lift-now]')`, 'Lift now', 15000)
  check('lockdown: the lift is approved with the passkey on the console', true)
  await evaluate(`(document.querySelector('[data-testid=lift-now]').click(), true)`)
  await waitFor(`location.pathname === '/'`, 'back to the console', 10000)
  await sleep(900)
  const after = await evaluate(`fetch('/api/brakes').then(r => r.json())`)
  const used = await evaluate(`fetch('/api/approvals').then(r => r.json()).then(j => (j.approvals || []).find(a => a.id === '${liftID}'))`)
  check('lockdown: Lift now lifts it, spending the approval', !after.state?.lockdown && used?.status === 'consumed' && !(await evaluate(`!!document.querySelector('[data-testid=brakes-banner]')`)),
    JSON.stringify([after.state, used?.status]))
} catch (e) {
  check('smoke ran to completion', false, e.message)
  const shot = await send('Page.captureScreenshot', { format: 'png' })
  if (shot.result?.data) fs.writeFileSync(`${HOME}/failure.png`, Buffer.from(shot.result.data, 'base64'))
}
const failed = results.filter((r) => !r.ok).length
console.log(`\n${results.length - failed}/${results.length} passed`)
ws.close(); chrome.kill(); process.exit(failed ? 1 : 0)
