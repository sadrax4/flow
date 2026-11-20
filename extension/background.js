// Relays between the ChatGPT tab and the Teleprompter app on this computer.
// Requests go from here, not the page, so the site's CORS and local-network rules don't apply.
let config;

async function getConfig() {
  config ??= await (await fetch(chrome.runtime.getURL('config.json'))).json();
  return config;
}

async function callApp(path, init = {}) {
  const { port, token } = await getConfig();
  const res = await fetch(`http://127.0.0.1:${port}${path}`, {
    ...init,
    headers: { 'X-TP-Token': token, 'Content-Type': 'application/json' },
  });
  if (res.status === 204) return null;
  if (!res.ok) throw new Error(`Teleprompter replied ${res.status}`);
  return res.json();
}

chrome.runtime.onMessage.addListener((msg, _sender, reply) => {
  const work = msg.type === 'wait'
    ? callApp('/wait')
    : callApp('/answer', { method: 'POST', body: JSON.stringify(msg.answer) });
  work.then(result => reply({ ok: true, result }), err => reply({ ok: false, error: String(err) }));
  return true;
});
