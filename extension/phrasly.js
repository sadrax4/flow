// Runs on phrasly.ai: once ChatGPT's answer to a snip is ready, puts it into the AI Humanizer
// (phrasly.ai/dashboard/humanizer, with the user's own settings), clicks Humanize and sends the humanized text
// back to the bar. Every step goes to Teleprompter's log, without the text itself.
// Wrapped in a function so a second copy in the same tab doesn't clash with the first one's names.
(() => {
  // After an extension update this file is injected again into open tabs; the newest copy takes over.
  const instance = Symbol('teleprompter-phrasly');
  window.teleprompterPhrasly = instance;
  const current = () => window.teleprompterPhrasly === instance && !!chrome.runtime?.id;

  // Waits through the extension's worker: in a background tab the page's own timers can slow to once a minute.
  async function sleep(ms) {
    try {
      await chrome.runtime.sendMessage({ type: 'sleep', ms });
    } catch {
      await new Promise(r => setTimeout(r, ms));
    }
  }

  function send(msg) {
    try {
      chrome.runtime.sendMessage(msg).catch(() => {});
    } catch {} // extension reloaded
  }
  const log = line => send({ type: 'log', line });
  const report = answer => send({ type: 'answer', answer });

  const visible = el => el.getClientRects().length > 0;
  const textOf = el => (!el ? '' : el instanceof HTMLTextAreaElement ? el.value : el.innerText);
  const wordCount = s => (s.trim().match(/\S+/g) ?? []).length;
  const same = (a, b) => a.replace(/\s+/g, ' ').trim() === b.replace(/\s+/g, ' ').trim();

  // An element as tag and attributes only, for the log.
  function describe(el) {
    if (!el) return 'none';
    const attrs = [...el.attributes].filter(a => a.name !== 'class' && a.name !== 'style')
      .map(a => ` ${a.name}="${a.value.slice(0, 40)}"`).join('');
    return `<${el.tagName.toLowerCase()}${attrs}>${visible(el) ? '' : ' (hidden)'}`;
  }

  async function waitFor(check, ms, failure) {
    const end = Date.now() + ms;
    for (;;) {
      if (!current()) throw new Error('replaced by a newer copy of the extension');
      const v = check();
      if (v) return v;
      if (Date.now() > end) throw new Error(failure);
      await sleep(200);
    }
  }

  // The first text on the page matching `re`.
  function findText(re) {
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      if (re.test(n.data) && n.parentElement && visible(n.parentElement)) return n;
    }
    return null;
  }

  // The input box: the one whose placeholder says "type or paste it here", else the largest visible text box.
  const hintOf = el => [el.getAttribute('placeholder'), el.getAttribute('aria-placeholder'), el.getAttribute('data-placeholder'),
    el.querySelector('[data-placeholder]')?.getAttribute('data-placeholder'), el.getAttribute('aria-label')].filter(Boolean).join(' ');
  const area = el => {
    const r = el.getBoundingClientRect();
    return r.width * r.height;
  };
  function inputBox() {
    const boxes = [...document.querySelectorAll('textarea, [contenteditable="true"]')].filter(el => visible(el) && !el.readOnly);
    return boxes.find(el => /paste it here/i.test(hintOf(el))) ?? boxes.sort((a, b) => area(b) - area(a))[0] ?? null;
  }

  // Buttons by their label: "Humanize" (or "Re-Humanize" once there is output), and "Stop" while it works.
  const label = b => (b.innerText || b.getAttribute('aria-label') || '').trim();
  const buttons = () => [...document.querySelectorAll('button, [role="button"]')].filter(visible);
  const humanizeButton = () => buttons().find(b => /^(re-?)?humanize$/i.test(label(b))) ?? null;
  const stopButton = () => buttons().find(b => /^stop$/i.test(label(b))) ?? null;
  const enabled = b => !!b && !b.disabled && b.getAttribute('aria-disabled') !== 'true';

  // The status line: "Waiting for your input..." when idle; "Humanizing content...", "Proofreading..." and the like
  // while it works.
  const STATUS = /waiting for your input|humanizing content|proofreading|checking content|saving draft|removing watermark|opening editor/i;
  const BUSY = /humanizing content|proofreading|checking content|removing watermark|opening editor/i;
  const statusText = () => findText(STATUS)?.data.trim() ?? '';

  // The output side: the highest ancestor of its placeholder ("Humanized content will appear here...") or of a
  // previous output that doesn't also hold the input box.
  function outputPane(input) {
    const start = findText(/content will appear here/i)?.parentElement
      ?? [...document.querySelectorAll('.whitespace-pre-wrap, [role="region"]')].find(el => visible(el) && !el.contains(input))
      ?? findText(/^\s*[\d,]+\s+words\s*$/i)?.parentElement;
    if (!start || start.contains(input)) return null;
    let pane = start;
    for (let el = start.parentElement; el && el !== document.body && !el.contains(input); el = el.parentElement) pane = el;
    return pane;
  }

  // The humanized text: the output's text block (Phrasly writes it into a whitespace-pre-wrap block), else the
  // biggest part of the output side with no buttons in it. The placeholder counts as no text.
  function outputText(pane) {
    if (!pane) return '';
    const blocks = [...pane.querySelectorAll('.whitespace-pre-wrap, [role="region"], textarea, [contenteditable="true"]')];
    const plain = [...pane.querySelectorAll('div, p, section, article')].filter(el => !el.querySelector('button'));
    const best = (list) => list.map(el => textOf(el).trim()).filter(t => t && !/content will appear here/i.test(t))
      .sort((a, b) => b.length - a.length)[0] ?? '';
    return best(blocks) || best(plain);
  }

  const busy = pane => !!stopButton() || BUSY.test(statusText()) || !!pane?.querySelector('[aria-busy="true"]');

  // Notices on screen (word limits, upgrade prompts, errors), for the error message and the log.
  function notices() {
    const out = [...document.querySelectorAll('[role="alert"], [role="status"], [role="tooltip"], [data-sonner-toast], .Toastify__toast')]
      .filter(visible).map(el => el.innerText.trim().replace(/\s+/g, ' ')).filter(t => t && !STATUS.test(t));
    const limit = findText(/minimum \d+ words|at least \d+ words|upgrade to continue|limit reached|no words left/i);
    if (limit) out.unshift(limit.data.trim());
    return out.slice(0, 4).map(t => t.slice(0, 160));
  }

  // The humanizer's structure only (tags, attributes and the labels of its own buttons; no text), for the log.
  function describePage(input, pane) {
    let main = input ?? document.body;
    while (pane && main !== document.body && !main.contains(pane)) main = main.parentElement;
    return [
      `page ${location.pathname}, tab ${document.visibilityState}`,
      `input: ${describe(input)}, status: "${statusText()}"`,
      `buttons: ${[...main.querySelectorAll('button')].filter(visible).slice(0, 25)
        .filter(b => !b.closest('nav, aside')).map(b => `${describe(b)}"${label(b).slice(0, 24)}"`).join(' | ')}`,
      `output side: ${describe(pane)} containing ${[...(pane?.querySelectorAll('*') ?? [])].filter(el => el.attributes.length).slice(0, 15).map(describe).join(' ')}`,
      `notices: ${notices().join(' | ') || 'none'}`,
    ].join('\n');
  }

  // Replaces the box's text the way typing would, so the page's own code sees the change.
  function setInput(el, text) {
    el.focus();
    if (el instanceof HTMLTextAreaElement) {
      el.select();
      if (!document.execCommand('insertText', false, text) || !same(el.value, text)) {
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(el, text);
        el.dispatchEvent(new Event('input', { bubbles: true }));
      }
      return;
    }
    window.getSelection().selectAllChildren(el);
    if (!document.execCommand('insertText', false, text)) {
      const dt = new DataTransfer();
      dt.setData('text/plain', text);
      el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    }
  }

  async function job(id, text) {
    const started = Date.now();
    const step = line => log(`snip #${id} +${((Date.now() - started) / 1000).toFixed(1)}s: phrasly: ${line}`);
    let input = null, pane = null;
    try {
      step(`humanizing ${wordCount(text)} words in tab ${location.pathname} (${document.visibilityState})`);
      input = await waitFor(inputBox, 15000, 'humanizer input box not found');
      if (busy(outputPane(input))) {
        step('the humanizer is still busy; waiting for it');
        await waitFor(() => !busy(outputPane(input)), 3 * 60 * 1000, 'the humanizer stayed busy');
      }
      pane = outputPane(input);
      const before = outputText(pane);
      setInput(input, text);
      await waitFor(() => same(textOf(inputBox()), text), 5000, 'could not put the answer into the input box');
      step(`answer put into ${describe(input)}`);
      report({ id, stage: 'humanizing' });

      const button = await waitFor(() => enabled(humanizeButton()) && humanizeButton(), 8000, '').catch(() => null);
      if (!button) {
        const why = notices()[0];
        throw new Error(why ? why : humanizeButton() ? `the Humanize button stayed disabled (the answer has ${wordCount(text)} words)` : 'Humanize button not found');
      }
      step(`clicking ${describe(button)}"${label(button)}"`);
      button.click();
      await waitFor(() => busy(pane) || outputText(outputPane(input)) !== before, 15000, '').catch(() => {
        throw new Error(notices()[0] ?? 'the humanizer did not start');
      });
      step(`working (status "${statusText()}")`);

      // Done once nothing is busy and the output has stayed the same for 2s (a hidden tab gets it all at the end).
      let last = '', since = Date.now();
      const result = await waitFor(() => {
        const out = outputText(outputPane(input));
        if (out !== last) {
          last = out;
          since = Date.now();
        }
        return !busy(outputPane(input)) && out && out !== before && Date.now() - since > 2000 ? out : null;
      }, 3 * 60 * 1000, 'the humanized text did not appear');
      step(`humanized: ${wordCount(result)} words`);
      report({ id, text: result, done: true, stage: 'humanized' });
    } catch (e) {
      if (!current()) return;
      let debug = '';
      try {
        debug = describePage(input, pane ?? (input && outputPane(input)));
      } catch (err) {
        debug = `could not describe the page: ${err}`;
      }
      report({ id, stage: 'humanize', error: e.message || String(e), debug });
    }
  }

  // One job at a time; while one runs, only the newest waiting snip is kept.
  let chain = Promise.resolve();
  let newest = 0;
  chrome.runtime.onMessage.addListener((msg, _sender, reply) => {
    if (msg?.type !== 'humanize' || !current()) return false;
    newest = msg.id;
    chain = chain.then(() => (msg.id === newest ? job(msg.id, msg.text) : log(`snip #${msg.id}: phrasly: skipped, a newer answer came`)));
    reply({ accepted: true });
    return false;
  });
  log(`phrasly: ready on ${location.pathname} (tab ${document.visibilityState})`);
})();
