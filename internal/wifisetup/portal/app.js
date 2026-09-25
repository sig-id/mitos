'use strict';
const form = document.querySelector('#setup'), network = document.querySelector('#network'),
  password = document.querySelector('#password'), join = document.querySelector('#join'),
  statusText = document.querySelector('#status'), reveal = document.querySelector('#reveal');
let token = '', busy = false, done = false, timer;
function message(text, error = false) { statusText.textContent = text; statusText.dataset.error = String(error); }
function controls() { join.disabled = !token || busy || done; password.disabled = busy || done; reveal.disabled = done; }
async function request(path, options = {}) {
  const controller = new AbortController(), timeout = setTimeout(() => controller.abort(), 4500);
  try { const r = await fetch(path, {cache: 'no-store', credentials: 'omit', signal: controller.signal, ...options});
    if (!r.ok) throw new Error('unavailable'); return await r.json();
  } finally { clearTimeout(timeout); }
}
function apply(s) {
  token = s.token || ''; network.value = s.ssid || '';
  busy = s.stage === 'joining';
  if (s.stage === 'connected') {
    done = true; token = ''; password.value = ''; password.type = 'password'; form.hidden = true;
    message('Connected. Your watch is ready. You can return to your usual Wi-Fi.');
  } else if (busy) message('Connecting… Keep this setup Wi-Fi connected.');
  else if (s.stage === 'error') message(s.message || 'Could not connect. Check the password and try again.', true);
  else message('Ready when you are.');
  controls();
}
async function poll() {
  clearTimeout(timer);
  try { apply(await request('/api/status')); }
  catch { token = ''; controls(); message(busy ? 'The connection is changing. Check your watch; reconnect to its setup Wi-Fi to retry.' : 'Reconnect to the watch’s setup Wi-Fi. If setup has ended, start From phone again.', true); }
  if (!done) timer = setTimeout(poll, 1500);
}
reveal.addEventListener('click', () => {
  const show = password.type === 'password'; password.type = show ? 'text' : 'password';
  reveal.textContent = show ? 'Hide' : 'Show'; reveal.setAttribute('aria-label', show ? 'Hide password' : 'Show password'); reveal.setAttribute('aria-pressed', String(show));
});
form.addEventListener('submit', async e => {
  e.preventDefault(); if (busy || done || !token) return;
  if (password.value && !/^[\x20-\x7e]{8,63}$/.test(password.value)) { message('Use 8–63 characters, or leave empty for an open network.', true); password.focus(); return; }
  clearTimeout(timer); busy = true; controls(); message('Connecting… Keep this setup Wi-Fi connected.');
  try { await request('/api/join', {method:'POST', headers:{'Content-Type':'application/json','X-Setup-Token':token}, body:JSON.stringify({ssid:network.value,password:password.value})}); }
  catch { message('Check your watch. Reconnect to its setup Wi-Fi if needed.', true); }
  timer = setTimeout(poll, 750);
});
window.addEventListener('pagehide', () => { password.value = ''; token = ''; clearTimeout(timer); });
poll();
