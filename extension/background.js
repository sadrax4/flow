// Relays between the ChatGPT tabs and the Teleprompter app on this computer.
// Requests go from here, not the page, so the site's CORS and local-network rules don't apply.
let config;
let queue = Promise.resolve(); // answers and log lines, sent to the app in order
const waiting = new Set(); // ports of ChatGPT tabs waiting for a snip
let spare = null; // a snip waiting for a tab: its tab closed as it arrived, or the selected tab is busy
let heldNoted = 0; // last snip logged as waiting, so it's noted once
const busy = new Set(); // tabs working on a snip; a tab is free again once it asks for the next one
const SPARE_MAX_AGE = 5 * 60 * 1000;

async function getConfig() {
  config ??= await (await fetch(chrome.runtime.getURL('config.json'))).json();
  return config;
}

async function callApp(path, init = {}) {
  const { port, token } = await getConfig();
  const res = await fetch(`http://127.0.0.1:${port}${path}`, {
    ...init,
    headers: {
      'X-TP-Token': token,
      'X-TP-Ext': chrome.runtime.getManifest().version, // lets the app spot an extension Chrome hasn't reloaded
      'Content-Type': 'application/json',
    },
  });
  if (res.status === 204) return null;
  if (res.status === 403) {
    config = undefined; // the app made a new key; read config.json again next time
    throw new Error('Teleprompter refused the key');
  }
  if (res.status === 409) {
    // Teleprompter carries a different copy of this extension and has just written it to disk: load it.
    chrome.runtime.reload();
    return null;
  }
  if (!res.ok) throw new Error(`Teleprompter replied ${res.status}`);
  return res.json();
}

// A tab working on a snip doesn't poll /wait, so while it does, the worker tells the app now and then that
// ChatGPT is still there; otherwise a snip taken during a long answer would be refused as "not connected".
let lastBeat = 0;
function heartbeat() {
  if (Date.now() - lastBeat < 10000) return;
  lastBeat = Date.now();
  callApp('/ping').catch(() => {});
}

function post(path, body) {
  queue = queue.then(() => callApp(path, { method: 'POST', body: JSON.stringify(body) })).catch(() => {});
}

const CHATGPT = ['https://chatgpt.com/*', 'https://chat.openai.com/*'];

// With several ChatGPT tabs open, the snip goes to the selected tab of the last focused window. A snip is never
// asked in a background tab while a selected ChatGPT tab is busy with the previous one: it waits for that tab.
async function pickPort() {
  const focused = await chrome.windows.getLastFocused().catch(() => null);
  let best = null;
  let bestScore = -1;
  for (const port of waiting) {
    const tab = await chrome.tabs.get(port.sender.tab.id).catch(() => null);
    const score = tab ? (tab.active ? 2 : 0) + (tab.windowId === focused?.id ? 1 : 0) : 0;
    if (score > bestScore) {
      best = port;
      bestScore = score;
    }
  }
  if (best && bestScore < 2) {
    const selected = await chrome.tabs.query({ active: true, url: CHATGPT }).catch(() => []);
    if (selected.some(tab => busy.has(tab.id))) return null;
  }
  return best;
}

async function dispatch(shot, at = Date.now()) {
  for (;;) {
    const port = await pickPort();
    if (!port) {
      if (waiting.size && heldNoted !== shot.id) {
        heldNoted = shot.id;
        post('/log', { line: `snip #${shot.id} waits for the selected ChatGPT tab, which is still busy` });
      }
      spare = { shot, at };
      return;
    }
    if (!waiting.has(port)) continue; // another snip took this tab while this one was choosing
    waiting.delete(port);
    try {
      port.postMessage({ shot });
      busy.add(port.sender.tab.id);
      return;
    } catch {} // that tab just closed; try another
  }
}

// Each wait lasts one long poll. Closing or reloading the tab cancels it, so the app keeps the snip for a live tab.
async function serve(port, signal) {
  let reply = {};
  try {
    const shot = await callApp('/wait', { signal });
    if (shot) await dispatch(shot);
  } catch (e) {
    reply = { error: String(e) };
  }
  if (waiting.delete(port)) {
    try {
      port.postMessage(reply); // no snip for this tab: it asks again
    } catch {}
  }
}

chrome.runtime.onConnect.addListener(port => {
  if (port.name !== 'wait' || !port.sender?.tab) return;
  const stop = new AbortController();
  busy.delete(port.sender.tab.id);
  waiting.add(port);
  port.onDisconnect.addListener(() => {
    waiting.delete(port);
    stop.abort();
  });
  if (spare) {
    const { shot, at } = spare;
    spare = null;
    if (Date.now() - at < SPARE_MAX_AGE) {
      dispatch(shot, at);
    } else {
      post('/answer', { id: shot.id, error: 'the ChatGPT tab stayed busy' });
    }
  }
  serve(port, stop.signal);
});

chrome.tabs.onRemoved.addListener(tabId => busy.delete(tabId));

// With a Phrasly AI Humanizer tab open, a finished answer goes there to be humanized; the bar then shows that version.
// Without one, the bar keeps ChatGPT's answer.
const PHRASLY = ['https://phrasly.ai/*', 'https://www.phrasly.ai/*'];
async function humanizerTab() {
  const tabs = await chrome.tabs.query({ url: PHRASLY }).catch(() => []);
  return tabs.find(t => /\/humanizer/i.test(new URL(t.url).pathname)) ?? null;
}
async function humanize(id, text) {
  const tab = await humanizerTab();
  if (!tab) {
    // The tab was closed while ChatGPT was answering: show ChatGPT's answer after all.
    post('/log', { line: `snip #${id}: the Phrasly humanizer tab is gone, so the bar shows ChatGPT's answer` });
    post('/answer', { id, text, done: true });
    return;
  }
  const message = { type: 'humanize', id, text };
  try {
    await chrome.tabs.sendMessage(tab.id, message);
  } catch {
    // The tab was opened before the extension was loaded: start the script there and try once more.
    await chrome.scripting.executeScript({ target: { tabId: tab.id }, files: ['phrasly.js'] }).catch(() => {});
    await chrome.tabs.sendMessage(tab.id, message).catch(() =>
      post('/answer', { id, stage: 'humanize', error: 'the Phrasly tab did not respond; refresh it', text }));
  }
}

// After an install or an update, start in the ChatGPT and Phrasly tabs that are already open, so they need no refresh.
chrome.runtime.onInstalled.addListener(async () => {
  for (const [urls, file] of [[CHATGPT, 'content.js'], [PHRASLY, 'phrasly.js']]) {
    const tabs = await chrome.tabs.query({ url: urls }).catch(() => []);
    for (const tab of tabs) {
      chrome.scripting.executeScript({ target: { tabId: tab.id }, files: [file] }).catch(() => {});
    }
  }
});

chrome.runtime.onMessage.addListener((msg, _sender, reply) => {
  switch (msg.type) {
    case 'sleep': // the worker's timers run at full speed even when the ChatGPT tab is in the background
      setTimeout(reply, msg.ms);
      heartbeat();
      return true;
    case 'log':
      post('/log', { line: msg.line });
      return false;
    case 'answer':
      post('/answer', msg.answer);
      return false;
    case 'humanize':
      humanize(msg.id, msg.text);
      return false;
    case 'humanizer':
      humanizerTab().then(tab => reply({ open: !!tab }));
      return true;
  }
  return false;
});
