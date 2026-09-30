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
  // Name where the page is: a browser that cannot load pages at all (a
  // sandboxed Chrome, say) sits on chrome-error:// or about:blank.
  const where = await evaluate(`location.href + ' (title ' + JSON.stringify(document.title) + ')'`).catch(() => 'unknown')
  throw new Error('timed out waiting for ' + what + ' at ' + where)
}
await send('Page.enable'); await send('Runtime.enable')
const go = async (url) => { await send('Page.navigate', { url }); await sleep(900); await evaluate(lib) }

// The console's own API, as the page calls it, for an expression evaluated
// in the page: the session cookie alone is not a session, so the key the
// sign-in left in localStorage goes with it (H6). Inlined, since a page the
// console navigates to itself has none of lib's helpers.
const api = `((path) => fetch(path, { headers: { 'X-Cerberus-Session-Key': localStorage.getItem('cerberus.session-key') || '' } }))`

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
  // Sign-in has landed once the page holds its session key; a cold Chrome
  // can take longer than go's pause, and every page after this needs it.
  await waitFor(`location.pathname !== '/login' && !!localStorage.getItem('cerberus.session-key')`, 'sign-in', 15000)
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
  const stops = await evaluate(`${api}('/api/approvals').then(r => r.json()).then(j => (j.approvals || []).filter(a => a.operation === 'stop'))`)
  const consumed = stops.filter((a) => a.status === 'consumed')
  check('resource stop: the right target runs, under one consumed approval decided by the console session',
    consumed.length === 1 && consumed[0].decision?.surface === 'tty_confirm' && consumed[0].decision?.by?.via === 'web' && !!consumed[0].decision?.by?.session && !/plan_stale/.test(errText),
    JSON.stringify(stops.map((a) => [a.status, a.decision?.surface, a.decision?.by?.via])))

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
  await waitFor(`${api}('/api/approvals').then(r => r.json()).then(j => (j.approvals || []).some(a => a.id === '${asked.approval_id}' && a.status === 'approved'))`, 'passkey approval', 15000)
  check('break glass: approved on the console with the passkey', true)
  const ran = await (await fetch(`http://127.0.0.1:4798/break-glass?id=${asked.approval_id}`)).json()
  check('break glass: the CLI retry runs under it', !ran.error && ran.result?.success !== false, ran.error || '')
  const open = await (await fetch('http://127.0.0.1:4798/follow-ups')).json()
  check('break glass: it stays a follow-up until acknowledged', open.length === 1 && open[0].id === asked.approval_id)
  await go('http://localhost:4799/')
  await waitFor(`!!document.querySelector('[data-testid=break-glass-alert]')`, 'header badge')
  check('break glass: the console header shows the badge', /BREAK GLASS · 1 today · 1 to acknowledge/.test(await evaluate(`document.querySelector('[data-testid=break-glass-alert]').textContent`)))

  // 5. The console's own writes (M9): saving a credential on the
  //    Credentials page is an admin write on a target with no labels of
  //    its own, so it needs an out-of-band approval, which the console asks
  //    for in place and approves with the passkey (I5: the passkey is the
  //    boundary), then saves under it. The editor lists only what the
  //    connector declares.
  await go('http://localhost:4799/credentials'); await evaluate(lib)
  const tokenInput = `[...document.querySelectorAll('label')].find(l => l.textContent.includes('token')).querySelector('input')`
  await waitFor(`!!(${tokenInput})`, 'credential editor: the declared token field')
  const listed = await evaluate(`${api}('/api/credentials').then(r => r.json())`)
  check('credentials: the editor lists the declared secret, named, with no value',
    listed.providers.length === 1 && listed.providers[0].id === 'demo' && listed.providers[0].secrets.length === 1 &&
      listed.providers[0].secrets[0].name === 'token' && listed.providers[0].secrets[0].kind === 'credential' &&
      listed.providers[0].secrets[0].present === false && !('value' in listed.providers[0].secrets[0]),
    JSON.stringify(listed))
  check('credentials: a credential is a password field', (await evaluate(`${tokenInput}.type`)) === 'password')
  await evaluate(`(__type(${tokenInput}, 'smoke-token-value'), true)`)
  await sleep(150)
  await evaluate(`(__btn(document, 'Save credentials').click(), true)`)
  await waitFor(`!!document.querySelector('[data-testid=out-of-band-step] [data-testid=approval-detail]')`, 'credential save: out-of-band step', 10000)
  const step = await evaluate(`document.querySelector('[data-testid=out-of-band-step]').textContent`)
  check('credential save: the console asks for the passkey approval in place, showing the write',
    /console\.provider_save/.test(step) && /out_of_band/.test(step) && /sha256:[0-9a-f]{64}/.test(step) && !step.includes('smoke-token-value'), step.slice(0, 160))
  const oob = `document.querySelector('[data-testid=out-of-band-step]')`
  await evaluate(`(__type(${oob}.querySelector('[data-testid=typed]'), 'demo'), true)`)
  await sleep(150)
  await evaluate(`(__btn(${oob}, 'Approve with passkey').click(), true)`)
  await waitFor(`!document.querySelector('[data-testid=out-of-band-step]')`, 'credential save: step to close after the save', 15000)
  const stored = await (await fetch('http://127.0.0.1:4798/stored?service=demo&key=token')).json()
  const credSaves = await evaluate(`${api}('/api/approvals').then(r => r.json()).then(j => (j.approvals || []).filter(a => a.connector === 'console' && a.operation === 'provider_save'))`)
  check('credential save: stored under the approval, decided on the console with the passkey (same surface)',
    stored.stored === true && credSaves.length === 1 && credSaves[0].status === 'consumed' && credSaves[0].decision?.same_surface === true && !!credSaves[0].decision?.key_fingerprint && credSaves[0].decision?.by?.via === 'web',
    JSON.stringify([stored, credSaves.map((a) => [a.status, a.decision?.same_surface, a.decision?.by?.via])]))
  await waitFor(`/Stored/.test(document.body.textContent)`, 'credential editor shows the value is stored', 10000)
  check('credentials: the editor shows the secret is stored, never its value', !(await evaluate(`document.body.innerHTML.includes('smoke-token-value')`)))

  // 5b. An out-of-band resource action: stopping prod-api from the console
  //     is approved in place with the passkey, and the stop is sent again
  //     under that approval, with no trip to the approvals page.
  await go('http://localhost:4799/resources'); await evaluate(lib)
  await waitFor(`[...document.querySelectorAll('tr')].some(r => r.textContent.includes('prod-api'))`, 'prod-api row')
  await evaluate(`[...document.querySelectorAll('tr')].find(r => r.textContent.includes('prod-api') && r.querySelector('td')).querySelector('td').click(), true`)
  await waitFor(`!!__btn(__top(), 'Stop')`, 'prod-api detail Stop')
  await evaluate(`(__btn(__top(), 'Stop').click(), true)`)
  const stopAck = `__dialogs().find(d => /only with your acknowledgment/.test(d.textContent))`
  await waitFor(`!!${stopAck}`, 'prod-api stop: ack dialog')
  await evaluate(`(__btn(${stopAck}, 'Stop').click(), true)`)
  await waitFor(`!!document.querySelector('[data-testid=out-of-band-step] [data-testid=approval-detail]')`, 'prod-api stop: out-of-band step', 10000)
  const stopStep = await evaluate(`document.querySelector('[data-testid=out-of-band-step]').textContent`)
  check('out-of-band stop: the console asks for the passkey approval in place', /local\.stop on prod-api/.test(stopStep) && /out_of_band/.test(stopStep) && (await evaluate('location.pathname')) === '/resources', stopStep.slice(0, 120))
  const stopOob = `document.querySelector('[data-testid=out-of-band-step]')`
  await evaluate(`(__type(${stopOob}.querySelector('[data-testid=typed]'), 'prod-api'), true)`)
  await sleep(150)
  await evaluate(`(__btn(${stopOob}, 'Approve with passkey').click(), true)`)
  await waitFor(`!document.querySelector('[data-testid=out-of-band-step]')`, 'prod-api stop: step to close after the stop', 15000)
  const stopPath = await evaluate('location.pathname')
  const oobStops = await evaluate(`${api}('/api/approvals').then(r => r.json()).then(j => (j.approvals || []).filter(a => a.operation === 'stop' && a.channel === 'out_of_band' && a.principal?.via === 'web'))`)
  check('out-of-band stop: sent again under the approval, decided on the console with the passkey (same surface)',
    oobStops.length === 1 && oobStops[0].status === 'consumed' && oobStops[0].decision?.same_surface === true && !!oobStops[0].decision?.key_fingerprint && stopPath === '/resources',
    JSON.stringify([stopPath, oobStops.map((a) => [a.status, a.decision?.same_surface])]))

  // 6. Lockdown (§12): one click and no phrase to engage, a red banner, a
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
  // The lift's passkey approval is met in place, on the console page.
  await waitFor(`!!document.querySelector('[data-testid=out-of-band-step] [data-testid=approval-detail]')`, 'lift: out-of-band step', 10000)
  check('lockdown: the lift is approved in place, not on another page', (await evaluate('location.pathname')) === '/')
  const liftOob = `document.querySelector('[data-testid=out-of-band-step]')`
  await evaluate(`(__type(${liftOob}.querySelector('[data-testid=typed]'), 'brake.lockdown'), true)`)
  await sleep(150)
  await evaluate(`(__btn(${liftOob}, 'Approve with passkey').click(), true)`)
  // Lifted, the page reloads.
  await waitFor(`!document.querySelector('[data-testid=brakes-banner]') && !document.querySelector('[data-testid=out-of-band-step]')`, 'lift to land', 15000)
  await sleep(900); await evaluate(lib)
  check('lockdown: the lift is approved with the passkey on the console', true)
  const liftID = await evaluate(`${api}('/api/approvals').then(r => r.json()).then(j => ((j.approvals || []).find(a => a.connector === 'brake' && a.operation === 'lift_lockdown') || {}).id)`)
  const after = await evaluate(`${api}('/api/brakes').then(r => r.json())`)
  const used = await evaluate(`${api}('/api/approvals').then(r => r.json()).then(j => (j.approvals || []).find(a => a.id === '${liftID}'))`)
  check('lockdown: lifted under the approval, spending it', !after.state?.lockdown && used?.status === 'consumed' && !(await evaluate(`!!document.querySelector('[data-testid=brakes-banner]')`)),
    JSON.stringify([after.state, used?.status]))

  // 7. The circuit breaker (§12, P5-c): an agent denied twice is
  //    suspended, the console shows it, and a person resets it by typing
  //    the phrase.
  const denied1 = await (await fetch('http://127.0.0.1:4798/agent-stop')).json()
  const denied2 = await (await fetch('http://127.0.0.1:4798/agent-stop')).json()
  const suspended = await (await fetch('http://127.0.0.1:4798/agent-stop')).json()
  check('breaker: two real denials, then the session is suspended, naming the reset',
    /policy_denied/.test(denied1.error || '') && /policy_denied/.test(denied2.error || '') && /session_suspended/.test(suspended.error || '') && /cerberus breaker reset sus_/.test(suspended.error || ''),
    (suspended.error || '').slice(0, 160))
  await go('http://localhost:4799/')
  await waitFor(`!!document.querySelector('[data-testid^=suspension-]')`, 'suspension banner')
  const susLine = await evaluate(`document.querySelector('[data-testid^=suspension-]').textContent`)
  check('breaker: the console banner shows the suspended session', /SUSPENDED/.test(susLine) && /agent over mcp_stdio/.test(susLine) && /2 policy denials/.test(susLine), susLine.slice(0, 120))
  const susID = await evaluate(`document.querySelector('[data-testid^=suspension-]').dataset.testid.replace('suspension-', '')`)
  await evaluate(`(document.querySelector('[data-testid=reset-${susID}]').click(), true)`)
  await waitFor(`!!document.querySelector('[data-testid=reset-typed]')`, 'reset dialog')
  await evaluate(`(__type(document.querySelector('[data-testid=reset-typed]'), 'reset'), true)`)
  await sleep(100)
  await evaluate(`(__btn(__top(), 'Reset').click(), true)`)
  await sleep(700)
  check('breaker: a wrong phrase resets nothing', await evaluate(`!!document.querySelector('[data-testid=suspension-${susID}]') && /reset ${susID}/.test(__top()?.textContent || '')`))
  await evaluate(`(__type(document.querySelector('[data-testid=reset-typed]'), 'reset ${susID}'), true)`)
  await sleep(100)
  await evaluate(`(__btn(__top(), 'Reset').click(), true)`)
  await sleep(1200); await evaluate(lib)
  const afterReset = await (await fetch('http://127.0.0.1:4798/agent-stop')).json()
  check('breaker: the typed phrase resets it, and the agent is back under policy',
    !(await evaluate(`!!document.querySelector('[data-testid^=suspension-]')`)) && /policy_denied/.test(afterReset.error || ''), (afterReset.error || '').slice(0, 100))

  // 8. An approval link (M7), as an MCP client is handed one: in a browser
  //    with no console session, it signs in for that approval only. The
  //    page is that approval, the passkey approves it, and the rest of the
  //    console is refused, on the server and in the page.
  const scopedAsk = await (await fetch('http://127.0.0.1:4798/break-glass')).json()
  const scopedID = scopedAsk.approval_id
  const link = await (await fetch(`http://127.0.0.1:4798/approval-url?id=${scopedID}`)).text()
  await send('Network.clearBrowserCookies')
  await evaluate(`(localStorage.clear(), true)`)
  await go(link)
  await waitFor(`!!localStorage.getItem('cerberus.session-key') && !!document.querySelector('[data-testid=scoped-approval]') && !!document.querySelector('[data-testid=approval-detail]')`, 'scoped sign-in', 15000)
  const landed = await evaluate(`(() => ({ path: location.pathname + location.search, banner: document.querySelector('[data-testid=scoped-approval]').textContent, rows: [...document.querySelectorAll('[data-testid^="approval-apr_"]')].map(r => r.dataset.testid) }))()`)
  check('approval link: it lands on that approval, and says the sign-in is for it only',
    landed.path === `/approvals?id=${scopedID}` && landed.banner.includes(scopedID) && /only/.test(landed.banner) && /cerberus web open/.test(landed.banner), JSON.stringify(landed).slice(0, 200))
  check('approval link: the list holds that approval and no other', landed.rows.length === 1 && landed.rows[0] === `approval-${scopedID}`, landed.rows.join(','))
  const elsewhere = await evaluate(`${api}('/api/resources').then(async r => ({ status: r.status, body: await r.text() }))`)
  check('approval link: the rest of the API is refused as scoped_session', elsewhere.status === 403 && /scoped_session/.test(elsewhere.body), `${elsewhere.status} ${elsewhere.body.slice(0, 100)}`)
  await evaluate(`(__type(document.querySelector('[data-testid=typed]'), 'prod-api'), true)`)
  await sleep(150)
  await evaluate(`(__btn(document.querySelector('[data-testid=approval-detail]'), 'Approve with passkey').click(), true)`)
  await waitFor(`${api}('/api/approvals').then(r => r.json()).then(j => (j.approvals || []).some(a => a.id === '${scopedID}' && a.status === 'approved'))`, 'scoped passkey approval', 15000)
  check('approval link: the passkey approves it there', true)
  await go('http://localhost:4799/resources')
  await waitFor(`!!document.querySelector('[data-testid=scoped-approval]')`, 'scoped page on another route')
  const other = await evaluate(`(() => ({ scoped: document.querySelector('[data-testid=scoped-approval]').textContent, rows: document.querySelectorAll('tr').length, text: document.body.innerText.length }))()`)
  check('approval link: another route shows the scoped page, not the console or a blank one',
    other.scoped.includes(scopedID) && other.text > 40 && !(await evaluate(`[...document.querySelectorAll('tr')].some(r => r.textContent.includes('web') && r.textContent.includes('LOCAL'))`)), JSON.stringify(other))
} catch (e) {
  check('smoke ran to completion', false, e.message)
  const shot = await send('Page.captureScreenshot', { format: 'png' })
  if (shot.result?.data) fs.writeFileSync(`${HOME}/failure.png`, Buffer.from(shot.result.data, 'base64'))
}
const failed = results.filter((r) => !r.ok).length
console.log(`\n${results.length - failed}/${results.length} passed`)
ws.close(); chrome.kill(); process.exit(failed ? 1 : 0)
