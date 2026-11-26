// Runs on phrasly.ai: once ChatGPT's answer to a snip is ready, puts it into the AI Humanizer
// (phrasly.ai/dashboard/humanizer, with the user's own settings), clicks Humanize and sends the humanized text
// back to the bar. Every step goes to Teleprompter's log, without the text itself.
// Two designs of the humanizer are handled. The current one shows an input page (the box, Paste Text, Humanize) that
// turns into a result page once Humanize is clicked: tabs, the output, Stop while it works, then Copy, Draft and a
// "Clear text" button (in a window under 1024px wide, "New text" at the top) that brings back an empty input box.
// The earlier one keeps the box and the output side by side, with a status line ("Waiting for your input...").
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
  // The output is written sentence by sentence, each with a space after it: no spaces before line breaks or doubled.
  const tidy = s => s.replace(/\u00a0/g, ' ').replace(/[ \t]+\n/g, '\n').replace(/[ \t]{2,}/g, ' ').trim();

  // Labels of Phrasly's own controls go to the log as they are; any other label (a title made from the text, say)
  // only as its length, so the log never holds the text.
  const UI_WORD = /^(re-?humanize|humanize|humanized|humanizing|with|ultra|lite|phrasly|stop|proofreading|paste|text|upload|import|from|file|check|checking|for|ai|copy|copied|clear|new|rewrite|strength|edit|in|docs|original|side|by|view|draft|previous|next|try|sample|max|easy|medium|aggressive|settings|open|close|about|the|a|any|app|or|click|tap|here|to|type|it|your|content|will|appear|score|detection|how|did|this|do|good|bad|[\W\d]*)$/i;
  const safe = s => (s.split(/\s+/).every(w => UI_WORD.test(w.replace(/[.:,…]+$/, ''))) ? s.slice(0, 40) : `(${wordCount(s)} words)`);

  // An element as tag and attributes only, for the log; attributes that can hold words go through safe().
  const WORDS_ATTR = /^(aria-label|aria-description|aria-placeholder|data-placeholder|placeholder|title|alt|value)$/;
  function describe(el) {
    if (!el) return 'none';
    const attrs = [...el.attributes].filter(a => a.name !== 'class' && a.name !== 'style')
      .map(a => ` ${a.name}="${WORDS_ATTR.test(a.name) ? safe(a.value) : a.value.slice(0, 40)}"`).join('');
    return `<${el.tagName.toLowerCase()}${attrs}>${visible(el) ? '' : ' (hidden)'}`;
  }

  // The snip whose answer is being humanized, and the newest one asked for: once a newer answer comes, the bar no
  // longer wants this one, so its job stops waiting and the newer one goes next.
  let active = 0;
  let newest = 0;
  const SUPERSEDED = 'a newer answer came';

  async function waitFor(check, ms, failure) {
    const end = Date.now() + ms;
    for (;;) {
      if (!current()) throw new Error('replaced by a newer copy of the extension');
      if (active !== newest) throw new Error(SUPERSEDED);
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

  // The humanizer's part of the page: its container, or in a window under 1024px wide the phone layout's frame.
  const root = () => document.getElementById('humanizer-container') ?? document.querySelector('[data-phone-shell]') ?? document.body;

  // The input box: the one whose placeholder reads "Paste text from any app, or click here to type." ("...tap to
  // type." in a narrow window; "...type or paste it here..." in the earlier design), else the humanizer's largest
  // visible text box.
  const INPUT_HINT = /paste text from any app|click here to type|tap to type|paste it here/i;
  const hintOf = el => [el.getAttribute('placeholder'), el.getAttribute('aria-placeholder'), el.getAttribute('data-placeholder'),
    el.querySelector('[data-placeholder]')?.getAttribute('data-placeholder'), el.getAttribute('aria-label')].filter(Boolean).join(' ');
  const area = el => {
    const r = el.getBoundingClientRect();
    return r.width * r.height;
  };
  function inputBox() {
    const boxes = [...document.querySelectorAll('textarea, [contenteditable="true"]')].filter(el => visible(el) && !el.readOnly && !el.closest('[inert]'));
    return boxes.find(el => INPUT_HINT.test(hintOf(el)))
      ?? boxes.filter(el => el instanceof HTMLTextAreaElement && root().contains(el)).sort((a, b) => area(b) - area(a))[0] ?? null;
  }

  // The Humanize button: the earlier design marks it data-humanizer-primary (and data-paraphrasing while it works);
  // otherwise the button labelled "Humanize" ("Humanize with Ultra" in a narrow window, "Re-Humanize" next to an
  // output). While Phrasly works, a button reads "Stop", then "Proofreading...".
  const label = b => (b.innerText.trim() || b.getAttribute('aria-label') || b.title || '').trim();
  // A button's text, or for an icon button its aria-label or tooltip.
  const named = (b, re) => [b.innerText, b.getAttribute('aria-label'), b.title].some(s => s && re.test(s.trim()));
  const buttons = () => [...document.querySelectorAll('button, [role="button"]')].filter(b => visible(b) && !b.closest('[inert]'));
  // The humanizer's own buttons, not those of the page around it (the sidebar comes first on the page).
  const own = match => buttons().find(b => root().contains(b) && match(b)) ?? null;
  const humanizeButton = () => own(b => b.hasAttribute('data-humanizer-primary') && !b.hasAttribute('data-paraphrasing'))
    ?? own(b => named(b, /^(re-?)?humanize( with ultra)?$/i));
  const stopButton = () => own(b => b.hasAttribute('data-paraphrasing') || named(b, /^(stop|proofreading\W*)$/i));
  // On a finished result there's no input box: this button (a bin icon) brings back an empty one. In a narrow window
  // it's "New text", in the page's header.
  const startOverButton = () => own(b => named(b, /^(clear|new) text$/i)) ?? buttons().find(b => named(b, /^new text$/i)) ?? null;
  const enabled = b => !!b && !b.disabled && b.getAttribute('aria-disabled') !== 'true';

  // Phrasly's own word count under the input ("38 / 5,000 Words"): it only changes once the page has taken the text.
  const countedWords = () => {
    const n = findText(/^\s*[\d,]+\s*\/\s*[\d,]+\s*words\s*$/i);
    return n ? Number(n.data.trim().split('/')[0].replace(/[^\d]/g, '')) : null;
  };
  const pasteButtonShown = () => buttons().some(b => /^paste text$/i.test(label(b)));
  // Taken: the count is up from zero, or (with no count on the page) the empty-box buttons are gone.
  const taken = () => {
    const n = countedWords();
    return n === null ? !pasteButtonShown() : n > 0;
  };

  // The earlier design's status line: "Waiting for your input..." when idle; "Humanizing content...",
  // "Proofreading..." and the like while it works.
  const STATUS = /waiting for your input|humanizing content|proofreading|checking content|saving draft|removing watermark|opening editor/i;
  const BUSY = /humanizing content|proofreading|checking content|removing watermark|opening editor/i;
  const statusText = () => findText(STATUS)?.data.trim() ?? '';

  // Phrasly writes its output, streamed and then final, into whitespace-pre-wrap blocks. These are the humanizer's
  // own: not the input box, the score panel or a dialog.
  const outputBlocks = () => [...root().querySelectorAll('.whitespace-pre-wrap, [role="region"]')].filter(el => visible(el)
    && !el.matches('textarea, [contenteditable="true"]') && !el.querySelector('textarea, [contenteditable="true"]')
    && !el.closest('aside, nav, header, [role="dialog"]'));
  // The humanized text: the longest of those blocks, leaving out the placeholder ("Humanized content will appear
  // here...") and the answer itself, which the Original view shows again.
  const outputText = answer => outputBlocks().map(el => tidy(el.innerText))
    .filter(t => t && !/content will appear here/i.test(t) && !same(t, answer))
    .sort((a, b) => b.length - a.length)[0] ?? '';

  // Not aria-busy: since the redesign, a background tab keeps the streamed text (marked busy) on screen after the
  // run until the tab is shown again, as its swap to the final text waits for an animation frame.
  const busy = () => !!stopButton() || BUSY.test(statusText());
  // What the humanizer shows: still busy, an input box, or a finished result with none (the current design).
  const phase = () => (busy() ? 'busy' : inputBox() ? 'input' : enabled(startOverButton()) ? 'result' : null);
  const REFUSAL = /at least \d+ words|under \d+ words|minimum \d+ words|upgrade to continue|limit reached|ran out|no words left|try again/i;

  // Notices on screen (word limits, upgrade prompts, errors), for the error message and the log.
  function notices() {
    const out = [...document.querySelectorAll('[role="alert"], [role="status"], [role="tooltip"], [data-sonner-toast], .Toastify__toast')]
      .filter(visible).map(el => el.innerText.trim().replace(/\s+/g, ' ')).filter(t => t && !STATUS.test(t));
    const limit = findText(/minimum \d+ words|at least \d+ words|upgrade to continue|limit reached|no words left/i);
    if (limit) out.unshift(limit.data.trim());
    return out.slice(0, 4).map(t => t.slice(0, 160));
  }

  // The humanizer's structure only (tags, attributes and the labels of its own buttons; no text), for the log.
  function describePage() {
    const r = root();
    return [
      `page ${location.pathname}, tab ${document.visibilityState}, humanizer: ${r === document.body ? 'no container' : describe(r)}, showing: ${phase() ?? 'nothing known'}`,
      `input: ${describe(inputBox())}, status: "${statusText()}", Phrasly counts ${countedWords() ?? '?'} words`,
      `buttons: ${buttons().filter(b => r.contains(b) && !b.closest('nav, aside')).slice(0, 25).map(b => `${describe(b)}"${safe(label(b))}"`).join(' | ')}`,
      `output blocks: ${outputBlocks().slice(0, 6).map(el => `${describe(el)} ${wordCount(el.innerText)} words`).join(', ') || 'none'}`,
      `notices: ${notices().map(safe).join(' | ') || 'none'}`,
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
    active = id;
    try {
      step(`humanizing ${wordCount(text)} words in tab ${location.pathname} (${document.visibilityState})`);
      let at = await waitFor(phase, 15000, 'humanizer input box not found');
      if (at === 'busy') {
        step('the humanizer is still busy; waiting for it');
        await waitFor(() => !busy(), 3 * 60 * 1000, 'the humanizer stayed busy');
        at = await waitFor(phase, 15000, 'humanizer input box not found');
      }
      if (at === 'result') {
        // The previous snip's result is showing, with no input box: start over, as "Clear text" does by hand.
        const clear = startOverButton();
        step(`a finished result is showing; clicking ${describe(clear)}"${safe(label(clear))}" for an empty input box`);
        clear.click();
        await waitFor(inputBox, 5000, 'no input box came back after clearing the previous result');
      }
      const input = inputBox();
      if (!input) throw new Error(`humanizer input box not found (${phase() ?? 'nothing known'} showing)`);
      const before = outputText(text);
      setInput(input, text);
      await waitFor(() => same(textOf(inputBox()), text), 5000, 'could not put the answer into the input box');
      // In a background tab the page can miss that edit (the box shows the text but Phrasly's own state stays
      // empty, and Humanize then does nothing). If its word count doesn't move, hand the text to the box's own
      // change handler, from inside the page.
      const counted = await waitFor(taken, 1500, '').catch(() => false);
      if (!counted) {
        const countBefore = countedWords();
        const how = await chrome.runtime.sendMessage({ type: 'phrasly-set', text }).catch(e => String(e));
        step(`the page didn't take the typed text (it counted ${countBefore ?? '?'} words); set it through its change handler: ${how}`);
        await waitFor(() => same(textOf(inputBox()), text) && taken(), 5000, `Phrasly didn't take the text (${how})`);
      }
      step(`answer put into ${describe(input)} (Phrasly counts ${countedWords() ?? '?'} words)`);
      report({ id, stage: 'humanizing' });

      const button = await waitFor(() => enabled(humanizeButton()) && humanizeButton(), 4000, '').catch(() => null);
      if (!button) {
        const why = notices()[0];
        throw new Error(why ? why : humanizeButton() ? `the Humanize button stayed disabled (the answer has ${wordCount(text)} words)` : 'Humanize button not found');
      }
      step(`clicking ${describe(button)}"${safe(label(button))}"`);
      // Phrasly answers a refusal ("Text must be at least 30 words.", word limits, upgrade prompts) with a toast;
      // only a message that wasn't on the page before the click counts.
      const shownBefore = new Set(notices());
      button.click();
      const refusal = () => notices().find(t => REFUSAL.test(t) && !shownBefore.has(t));
      await waitFor(() => busy() || outputText(text) !== before || refusal(), 12000, '').catch(() => {
        throw new Error(notices()[0] ?? 'the humanizer did not start');
      });
      if (!busy() && refusal()) throw new Error(refusal());
      step(`working (${stopButton() ? `"${safe(label(stopButton()))}" showing` : `status "${statusText()}"`})`);

      // Done once nothing is busy and the output has stayed the same for 2s (a hidden tab gets it all at the end).
      let last = '', since = Date.now();
      const result = await waitFor(() => {
        const out = outputText(text);
        if (out !== last) {
          last = out;
          since = Date.now();
        }
        return !busy() && out && out !== before && Date.now() - since > 2000 ? out : null;
      }, 3 * 60 * 1000, 'the humanized text did not appear');
      step(`humanized: ${wordCount(result)} words`);
      report({ id, text: result, done: true, stage: 'humanized' });
    } catch (e) {
      if (!current()) return;
      if (e.message === SUPERSEDED) {
        step(`stopped waiting: ${SUPERSEDED}`);
        return;
      }
      let debug = '';
      try {
        debug = describePage();
      } catch (err) {
        debug = `could not describe the page: ${err}`;
      }
      // ChatGPT's own answer goes along, for the bar to show since Phrasly couldn't humanize it.
      report({ id, stage: 'humanize', error: e.message || String(e), debug, text });
    }
  }

  // One job at a time; a newer snip's answer ends the one running and goes next, and only the newest waits.
  let chain = Promise.resolve();
  chrome.runtime.onMessage.addListener((msg, _sender, reply) => {
    if (msg?.type !== 'humanize' || !current()) return false;
    newest = msg.id;
    chain = chain.then(() => (msg.id === newest ? job(msg.id, msg.text) : log(`snip #${msg.id}: phrasly: skipped, a newer answer came`)));
    reply({ accepted: true });
    return false;
  });
  log(`phrasly: ready on ${location.pathname} (tab ${document.visibilityState})`);
})();
