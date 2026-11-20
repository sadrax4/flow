// Relays between the ChatGPT tabs and the Teleprompter app on this computer.
// Requests go from here, not the page, so the site's CORS and local-network rules don't apply.
let config;
let queue = Promise.resolve(); // answers and log lines, sent to the app in order
const waiting = new Set(); // ports of ChatGPT tabs waiting for a snip
let spare = null; // a snip whose tab closed as it arrived, kept briefly for the next tab

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
  if (res.status === 409) {
    // Teleprompter carries a different copy of this extension and has just written it to disk: load it.
    chrome.runtime.reload();
    return null;
  }
  if (!res.ok) throw new Error(`Teleprompter replied ${res.status}`);
  return res.json();
}

function post(path, body) {
  queue = queue.then(() => callApp(path, { method: 'POST', body: JSON.stringify(body) })).catch(() => {});
}

// With several ChatGPT tabs open, the snip goes to the selected tab of the last focused window.
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
  return best;
}

async function dispatch(shot) {
  for (;;) {
    const port = await pickPort();
    if (!port) {
      spare = { shot, at: Date.now() };
      return;
    }
    waiting.delete(port);
    try {
      port.postMessage({ shot });
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
  waiting.add(port);
  port.onDisconnect.addListener(() => {
    waiting.delete(port);
    stop.abort();
  });
  if (spare) {
    const { shot, at } = spare;
    spare = null;
    if (Date.now() - at < 60000) dispatch(shot);
  }
  serve(port, stop.signal);
});

// After an install or an update, start in the ChatGPT tabs that are already open, so they need no refresh.
chrome.runtime.onInstalled.addListener(async () => {
  const tabs = await chrome.tabs.query({ url: ['https://chatgpt.com/*', 'https://chat.openai.com/*'] }).catch(() => []);
  for (const tab of tabs) {
    chrome.scripting.executeScript({ target: { tabId: tab.id }, files: ['content.js'] }).catch(() => {});
  }
});

chrome.runtime.onMessage.addListener((msg, _sender, reply) => {
  switch (msg.type) {
    case 'sleep': // the worker's timers run at full speed even when the ChatGPT tab is in the background
      setTimeout(reply, msg.ms);
      return true;
    case 'log':
      post('/log', { line: msg.line });
      return false;
    case 'answer':
      post('/answer', msg.answer);
      return false;
  }
  return false;
});
