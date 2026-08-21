/* USSD Lab simulator UI.
 *
 * Talks to the same /api endpoints as any other client -- the browser adds no
 * server surface of its own. It sends only a service code, a phone number, a
 * session id and user input; it never supplies a callback URL or a project id,
 * because the server resolves those from validated configuration (ADR-004).
 */
'use strict';

const $ = (id) => document.getElementById(id);

const el = {
  project:   $('project'),
  dialer:    $('view-dialer'),
  session:   $('view-session'),
  error:     $('view-error'),
  code:      $('service-code'),
  dial:      $('btn-dial'),
  dialHint:  $('dial-hint'),
  screen:    $('screen'),
  replyArea: $('reply-area'),
  reply:     $('reply'),
  send:      $('btn-send'),
  cancel:    $('btn-cancel'),
  endArea:   $('end-area'),
  newBtn:    $('btn-new'),
  errCode:   $('err-code'),
  errMsg:    $('err-message'),
  errHint:   $('err-hint'),
  dismiss:   $('btn-dismiss'),
  status:    $('status'),
  dot:       $('status-dot'),
  statusTxt: $('status-text'),
  statusSid: $('status-sid'),
};

const state = {
  view: 'dialer',      // dialer | session | error
  sessionId: null,
  status: null,
  ended: false,
  busy: false,
};

/* ---------------------------------------------------------------- api --- */

async function post(path, body) {
  let res;
  try {
    res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
  } catch (cause) {
    // The request never reached USSD Lab -- almost always the phone dropping
    // off the Wi-Fi network, which is worth saying plainly.
    throw {
      code: 'NETWORK_ERROR',
      message: 'Could not reach USSD Lab.',
      hint: 'Check that this phone is still on the same Wi-Fi network as your laptop.',
    };
  }

  let data = null;
  try { data = await res.json(); } catch (_) { /* tolerated below */ }

  if (!res.ok) {
    const err = (data && data.error) || {};
    throw {
      code: err.code || `HTTP_${res.status}`,
      message: err.message || 'The simulator returned an unexpected response.',
      hint: err.hint || '',
    };
  }
  return data;
}

/* -------------------------------------------------------------- render --- */

function render() {
  el.dialer.hidden  = state.view !== 'dialer';
  el.session.hidden = state.view !== 'session';
  el.error.hidden   = state.view !== 'error';

  el.replyArea.hidden = state.ended;
  el.endArea.hidden   = !state.ended;

  el.dial.disabled   = state.busy;
  el.send.disabled   = state.busy;
  el.cancel.disabled = state.busy;

  const showStatus = state.sessionId !== null;
  el.status.hidden = !showStatus;

  if (showStatus) {
    el.dot.dataset.status = state.status || '';
    el.statusTxt.textContent = state.status || '';
    el.statusSid.textContent = state.sessionId;
  }
}

/* Every value below reaches the DOM through textContent, never a markup sink.
 * Screen text comes from the developer's application and is untrusted: it must
 * be rendered as text, never parsed as markup. */
function showScreen(data) {
  state.view = 'session';
  state.sessionId = data.session_id;
  state.status = data.status;
  state.ended = data.type === 'END';

  el.screen.textContent = data.text || '';

  if (!state.ended) {
    el.reply.value = '';
    // Focus is skipped on touch devices: opening the keyboard immediately
    // hides the menu the user is trying to read.
    if (!matchMedia('(pointer: coarse)').matches) el.reply.focus();
  }
  render();
}

function showError(err) {
  state.view = 'error';
  el.errCode.textContent = err.code || '';
  el.errMsg.textContent = err.message || '';
  el.errHint.textContent = err.hint || '';

  // A dead session should not look live in the status bar.
  if (['SESSION_TIMEOUT', 'SESSION_NOT_FOUND', 'SESSION_NOT_ACTIVE'].includes(err.code)) {
    state.status = err.code === 'SESSION_TIMEOUT' ? 'TIMEOUT' : null;
    state.ended = true;
  }
  render();
}

function reset() {
  state.view = 'dialer';
  state.sessionId = null;
  state.status = null;
  state.ended = false;
  el.reply.value = '';
  el.dialHint.textContent = '';
  render();
  if (!matchMedia('(pointer: coarse)').matches) el.code.focus();
}

/* ------------------------------------------------------------- actions --- */

async function withBusy(fn) {
  if (state.busy) return;          // guards double-tap on a slow network
  state.busy = true;
  render();
  try {
    await fn();
  } catch (err) {
    showError(err);
  } finally {
    state.busy = false;
    render();
  }
}

function dial() {
  const code = el.code.value.trim();
  if (!code) {
    el.dialHint.textContent = 'Enter a service code, for example *124#';
    return;
  }
  withBusy(async () => {
    showScreen(await post('/api/dial', { service_code: code }));
  });
}

function send() {
  const text = el.reply.value.trim();
  if (!text) return;
  withBusy(async () => {
    showScreen(await post('/api/input', { session_id: state.sessionId, text }));
  });
}

function cancel() {
  if (!state.sessionId) return reset();
  withBusy(async () => {
    await post('/api/cancel', { session_id: state.sessionId });
    reset();
  });
}

/* --------------------------------------------------------------- wiring -- */

el.dial.addEventListener('click', dial);
el.send.addEventListener('click', send);
el.cancel.addEventListener('click', cancel);
el.newBtn.addEventListener('click', reset);
el.dismiss.addEventListener('click', reset);

el.code.addEventListener('keydown', (e) => { if (e.key === 'Enter') dial(); });
el.reply.addEventListener('keydown', (e) => { if (e.key === 'Enter') send(); });

/* Prefill the service code this project answers, so the developer does not
 * have to remember it. */
(async function init() {
  render();
  let attached = false;
  try {
    const res = await fetch('/api/info');
    attached = res.ok;
    const info = await res.json();
    el.project.textContent = info.project || '';
    if (info.service_code) {
      el.code.value = info.service_code;
      el.code.placeholder = info.service_code;
    }
  } catch (_) {
    // Prefill is a convenience; the dialer works without it.
  }
  if (!attached) {
    showError({
      code: 'ATTACH_REQUIRED',
      message: 'This device is not attached to the simulator.',
      hint: 'Scan the QR code shown by `ussd dev`.',
    });
    return;
  }
  if (!matchMedia('(pointer: coarse)').matches) el.code.focus();
})();
