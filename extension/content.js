// Runs on chatgpt.com: takes each screenshot from Teleprompter, sends it with the prompt,
// and streams the answer text back to the bar as it is written.
// Selectors cover ChatGPT's current composer (data-mobile-composer-*) and the older one (#prompt-textarea).
// Every step goes to Teleprompter's log (%APPDATA%\Teleprompter\chatgpt-log.txt), without any chat text.
// Wrapped in a function so a second copy in the same tab doesn't clash with the first one's names.
(() => {
  // After an extension update this file is injected again into open tabs; the newest copy takes over.
  const instance = Symbol('teleprompter');
  window.teleprompterInstance = instance;
  const current = () => window.teleprompterInstance === instance && !!chrome.runtime?.id;

  const PENDING = 'teleprompter-pending-answer';
  const SEND = 'button[data-composer-submit], [data-testid="send-button"], #composer-submit-button, ' +
    'button[type="submit"], button[aria-label*="Send" i]';

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

  // Also looks inside open shadow roots, in case parts of the page move into web components.
  function findAll(sel) {
    const out = [...document.querySelectorAll(sel)];
    if (out.length) return out;
    const walk = root => {
      for (const el of root.querySelectorAll('*')) {
        if (!el.shadowRoot) continue;
        out.push(...el.shadowRoot.querySelectorAll(sel));
        walk(el.shadowRoot);
      }
    };
    walk(document);
    return out;
  }
  const visible = el => el.getClientRects().length > 0;

  // An element as tag and attributes only, for the log.
  function describe(el) {
    if (!el) return 'none';
    const attrs = [...el.attributes].filter(a => a.name !== 'class' && a.name !== 'style')
      .map(a => ` ${a.name}="${a.value.slice(0, 40)}"`).join('');
    return `<${el.tagName.toLowerCase()}${attrs}>${visible(el) ? '' : ' (hidden)'}`;
  }

  // The page with chat and project ids masked, and whether the tab is on screen.
  function where() {
    const path = location.pathname.split('/').map(s => (/\d/.test(s) && s.length > 12 ? '<id>' : s)).join('/');
    return `${path} (tab ${document.visibilityState}${document.hasFocus() ? ', focused' : ''})`;
  }

  // Known composer markers first; otherwise the visible text box lowest on the page.
  function editor() {
    const known = findAll('[data-mobile-composer-prompt], #prompt-textarea, textarea[name="prompt"], ' +
      '[contenteditable="true"][data-composer-markdown]').find(visible);
    if (known) return known;
    return findAll('textarea, [contenteditable="true"], [role="textbox"]')
      .filter(el => visible(el) && el.getBoundingClientRect().width > 200)
      .sort((a, b) => b.getBoundingClientRect().bottom - a.getBoundingClientRect().bottom)[0] || null;
  }

  // The box's form, or else its nearest ancestor that also holds a send button.
  function composer() {
    const ed = editor();
    if (!ed) return null;
    if (ed.form) return ed.form;
    for (let el = ed.parentElement, i = 0; el && i < 10; el = el.parentElement, i++) {
      if (el.tagName === 'FORM' || el.querySelector(SEND)) return el;
    }
    return ed.parentElement;
  }

  // Chat messages and their role: data-message-role (web composer), data-message-author-role (older page),
  // data-conversation-role (the logged-in app).
  const MESSAGE = '[data-message-role], [data-message-author-role], [data-conversation-role]';
  const roleOf = el => (el.getAttribute('data-message-role') ?? el.getAttribute('data-message-author-role')
    ?? el.getAttribute('data-conversation-role') ?? '').toLowerCase();
  const assistantMessages = () => findAll(MESSAGE).filter(el => roleOf(el) !== 'user');
  const userMessages = () => findAll(MESSAGE).filter(el => roleOf(el) === 'user');
  const generating = () => findAll(
    '[data-message-role="assistant"][data-message-streaming]:not([data-message-complete]), ' +
    'button[data-composer-submit][data-stop-generating], [data-testid="stop-button"], button[aria-label^="Stop" i]').length > 0;

  function anySendButton() {
    const isStop = b => b.hasAttribute('data-stop-generating') || b.matches('[data-testid="stop-button"]')
      || /^stop/i.test(b.getAttribute('aria-label') || '');
    return [...(composer() || document).querySelectorAll(SEND)].find(b => !isStop(b))
      || findAll('[data-testid="send-button"], #composer-submit-button').find(b => !isStop(b)) || null;
  }

  function sendButton() {
    const b = anySendButton();
    const ready = b && !b.disabled && !b.hidden && !b.hasAttribute('data-visually-disabled')
      && b.getAttribute('aria-disabled') !== 'true';
    return ready ? b : null;
  }

  // Page structure only (element and attribute names, no chat text), logged by the app when a step fails.
  function describePage() {
    const names = {};
    let shadowRoots = 0;
    for (const el of document.querySelectorAll('*')) {
      if (el.shadowRoot) shadowRoots++;
      for (const a of el.attributes) if (a.name.startsWith('data-')) names[a.name] = (names[a.name] || 0) + 1;
    }
    const notices = findAll('[role="alert"], [role="dialog"], [data-sonner-toast]').filter(visible).slice(0, 4)
      .map(el => el.innerText.trim().replace(/\s+/g, ' ').slice(0, 160)).filter(Boolean);
    return [
      `extension ${chrome.runtime.getManifest().version}, page ${where()}, shadow roots ${shadowRoots}`,
      'text boxes: ' + findAll('textarea, [contenteditable="true"], [role="textbox"]').map(describe).join(' | '),
      'composer: ' + describe(composer()),
      'composer buttons: ' + [...(composer()?.querySelectorAll('button') ?? [])].slice(0, 15).map(describe).join(' | '),
      'notices on screen: ' + (notices.join(' | ') || 'none'),
      'newest messages: ' + (findAll(MESSAGE).slice(-4).map(m => `${describe(m)} containing ` +
        [...m.querySelectorAll('*')].filter(el => [...el.attributes].some(a => a.name.startsWith('data-'))).slice(0, 8).map(describe).join(' ')
      ).join(' || ') || 'none'),
      'composer area: ' + describeComposer(),
      'data attributes: ' + Object.entries(names).sort((a, b) => b[1] - a[1]).slice(0, 120).map(([n, c]) => `${n}:${c}`).join(' '),
    ].join('\n');
  }

  // The whole composer area: the box's highest ancestor that is still a small part of the window,
  // so it takes in the row of attachment previews but not the conversation.
  function composerArea() {
    const ed = editor();
    if (!ed) return null;
    let area = ed;
    for (let el = ed.parentElement; el && el !== document.body; el = el.parentElement) {
      if (el.getBoundingClientRect().height > innerHeight * 0.45) break;
      area = el;
    }
    return area;
  }

  // Hidden attachment fields (logged-in web composer: fileAttachments, pendingFileUploads; logged out:
  // imageAttachments, pendingImageUploads) and upload flags such as data-octane-auth-file-upload-pending.
  const attachmentFields = area => [...area.querySelectorAll('input[type="hidden"]')].filter(i => /attachment|upload/i.test(i.name));
  const filled = v => !!v && v !== '[]' && v !== '{}';
  const pendingFlags = area => [area, ...area.querySelectorAll('*')]
    .flatMap(el => [...el.attributes].filter(a => /upload-pending/.test(a.name)).map(a => a.name));

  // Changes as soon as an image is pasted: attachment fields or flags, the composer's layout (the app switches
  // to a stacked layout with a row of previews), or the number of elements and pictures in the composer.
  function attachmentState(forLog = false) {
    const area = composerArea();
    if (!area) return 'no composer';
    const layout = [];
    for (let el = editor(); el && el !== area.parentElement; el = el.parentElement) {
      for (const a of el.attributes) if (/layout|rows|attach|upload/i.test(a.name)) layout.push(`${a.name}=${a.value}`);
    }
    return [
      ...attachmentFields(area).map(i => `${i.name}=${forLog ? i.value.length : i.value}`),
      ...pendingFlags(area),
      ...layout,
      `elements=${area.querySelectorAll('*').length}`,
      `pictures=${area.querySelectorAll('img, canvas, video, [style*="background-image"]').length}`,
    ].join(' ');
  }

  // Still uploading: ChatGPT ignores a send until every pending upload is done.
  function uploading(area) {
    return pendingFlags(area).length > 0 || attachmentFields(area).some(i => /^pending/i.test(i.name) && filled(i.value));
  }
  const attachedAndUploaded = area =>
    !uploading(area) && attachmentFields(area).some(i => /attachments$/i.test(i.name) && filled(i.value));

  // Waits until a value stops changing for `quietMs` (an upload's progress shows as changes in the composer).
  async function settle(read, quietMs, maxMs) {
    const end = Date.now() + maxMs;
    let last = read(), since = Date.now();
    while (Date.now() < end && Date.now() - since < quietMs) {
      await sleep(150);
      const now = read();
      if (now !== last) {
        last = now;
        since = Date.now();
      }
    }
  }

  // The composer's elements with their attributes (no text), for the log when the paste isn't seen.
  const describeComposer = () => [...(composerArea()?.querySelectorAll('*') ?? [])]
    .filter(el => el.attributes.length).slice(0, 40).map(describe).join(' | ');

  // Long chats keep only part of the history on the page, so the answer is found as an assistant message that
  // wasn't there before the question was sent (by its id, which a redrawn old message keeps), not by counting.
  // Only messages below the newest question count, in case old answers get redrawn.
  const messageKey = el => el.id || el.getAttribute('data-turn-key') || el.getAttribute('data-dil-message-id')
    || el.getAttribute('data-message-id') || el.closest('[data-turn-key]')?.getAttribute('data-turn-key') || el;
  const keysOf = list => new Set(list.map(messageKey));
  const fresh = (list, known) => list.filter(m => !known.has(messageKey(m)));
  // Once the question just sent is on the page, any answer below it is ours; until then (or after a page change)
  // an answer counts only if it is new and below the newest question.
  function freshAnswers(known, questionsBefore) {
    const ours = questionsBefore ? fresh(userMessages(), questionsBefore).at(-1) : undefined;
    const question = ours ?? userMessages().at(-1);
    return assistantMessages().filter(m => (!question || question.compareDocumentPosition(m) & Node.DOCUMENT_POSITION_FOLLOWING)
      && (ours || !known.has(messageKey(m))));
  }

  // The answer to the question just sent: a new message below it. If none shows up for 15s (a page that reuses
  // message ids), the last answer on the page once its text differs from the last answer before sending.
  function ourAnswer(ctx) {
    const answers = freshAnswers(ctx.known, ctx.questionsBefore);
    if (answers.length) return answers.at(-1);
    if (!ctx.sentAt || Date.now() - ctx.sentAt < 15000) return null;
    const last = assistantMessages().at(-1);
    const text = last ? answerText(last) : '';
    return text && text !== ctx.lastBefore ? last : null;
  }

  async function waitFor(check, ms, failure) {
    const end = Date.now() + ms;
    for (;;) {
      if (!current()) throw new Error('replaced by a newer copy of the extension');
      const v = check();
      if (v) return v;
      if (Date.now() > end) throw new Error(failure);
      await sleep(150);
    }
  }

  function pasteFile(file) {
    const dt = new DataTransfer();
    dt.items.add(file);
    editor().focus();
    editor().dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
  }

  // An emptied rich-text box still holds a line break, so whitespace doesn't count.
  const boxLength = el => (!el ? '?' : (el instanceof HTMLTextAreaElement ? el.value : el.innerText).trim().length);

  function clearBox() {
    const el = editor();
    el.focus();
    if (el instanceof HTMLTextAreaElement) {
      el.select();
      if (!document.execCommand('delete')) {
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(el, '');
        el.dispatchEvent(new Event('input', { bubbles: true }));
      }
      return;
    }
    window.getSelection().selectAllChildren(el);
    document.execCommand('delete');
  }

  function typeAtEnd(text) {
    const el = editor();
    el.focus();
    if (el instanceof HTMLTextAreaElement) {
      el.setSelectionRange(el.value.length, el.value.length);
      if (!document.execCommand('insertText', false, text)) {
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(el, el.value + text);
        el.dispatchEvent(new Event('input', { bubbles: true }));
      }
      return;
    }
    const sel = window.getSelection();
    sel.selectAllChildren(el);
    sel.collapseToEnd();
    document.execCommand('insertText', false, text);
  }

  // An answer's text block. The role marker is either the whole message (web composer) or only its
  // "ChatGPT said:" heading (the logged-in app); then the answer is the last response block after the heading,
  // before the next message. Button labels and anything outside those blocks stay out.
  const BODY = '[data-response-body], [data-assistant-markdown], .markdown';
  function answerBody(msg) {
    const inside = msg.querySelectorAll(BODY);
    if (inside.length) return inside[inside.length - 1];
    const turn = msg.closest('[data-turn-key], [data-virtualized-turn]');
    return (turn && bodyAfter(msg, [...turn.querySelectorAll(`${MESSAGE}, ${BODY}`)])) || bodyAfter(msg, findAll(`${MESSAGE}, ${BODY}`));
  }
  function bodyAfter(msg, list) {
    let after = false, body = null;
    for (const el of list) {
      if (el === msg) {
        after = true;
      } else if (after) {
        if (el.matches(MESSAGE) && !msg.contains(el)) break;
        if (el.matches(BODY) && !body?.contains(el)) body = el;
      }
    }
    return body;
  }
  function answerText(msg) {
    const body = answerBody(msg);
    if (body) return body.innerText.trim();
    return /^H[1-6]$/.test(msg.tagName) ? '' : msg.innerText.trim();
  }

  function stepper(id) {
    const start = Date.now();
    return text => log(`snip #${id} +${((Date.now() - start) / 1000).toFixed(1)}s: ${text}`);
  }

  async function handle(shot, step) {
    step(`reached the ChatGPT tab ${where()}`);
    const box = await waitFor(editor, 15000, 'chat box not found');
    step(`chat box ${describe(box)} in ${describe(composer())}`);
    const ctx = {}; // what was on the page just before sending, filled in below
    const bytes = Uint8Array.from(atob(shot.png), c => c.charCodeAt(0));
    const file = new File([bytes], `screenshot-${shot.id}.png`, { type: 'image/png' });

    const leftover = boxLength(editor());
    if (leftover) {
      clearBox();
      step(`cleared ${leftover} characters left in the chat box (now ${boxLength(editor())})`);
    }
    // Paste like a user would, once: ChatGPT takes pasted images, and attaching another way as well added it twice.
    const before = attachmentState();
    step(`composer before pasting: ${attachmentState(true)}`);
    pasteFile(file);
    try {
      await waitFor(() => attachmentState() !== before, 10000, '');
      step(`image pasted (${attachmentState(true)})`);
    } catch {
      step(`no sign of the pasted image after 10s; going on. Composer: ${describeComposer()}`);
    }
    const area = composerArea();
    if (area && attachmentFields(area).length) {
      await waitFor(() => composerArea() && attachedAndUploaded(composerArea()), 2 * 60 * 1000, 'image upload did not finish');
    } else {
      await settle(attachmentState, 1500, 30000);
    }
    step(`upload finished (${attachmentState(true)})`);
    if (shot.prompt) {
      typeAtEnd(shot.prompt);
      step(`prompt typed, the box now holds ${boxLength(editor())} characters`);
    }
    if (generating()) {
      step('ChatGPT is still answering the previous question; waiting for it to finish');
      await waitFor(() => !generating(), 5 * 60 * 1000, 'ChatGPT kept answering the previous question');
    }
    ctx.known = keysOf(assistantMessages());
    ctx.lastBefore = answerText(assistantMessages().at(-1) ?? document.createElement('i'));
    ctx.questionsBefore = keysOf(userMessages());
    // Sending may load a new page; the copy of this script there picks the answer up from this note.
    const ids = set => [...set].filter(k => typeof k === 'string');
    sessionStorage.setItem(PENDING, JSON.stringify({ id: shot.id, at: Date.now(), known: ids(ctx.known), questions: ids(ctx.questionsBefore) }));
    await submit(step, ctx.questionsBefore);
    ctx.sentAt = Date.now();
    // In a background tab ChatGPT may only show the answer once it is complete, and thinking can take minutes.
    let described = false;
    await waitFor(() => {
      if (!described && Date.now() - ctx.sentAt > 20000) {
        described = true;
        step(`no new answer on the page after 20s; the page now:\n${describePage()}`);
      }
      return ourAnswer(ctx);
    }, 4 * 60 * 1000, 'no answer');
    step(`answer started in ${describe(ourAnswer(ctx))}`);
    await streamAnswer(shot.id, step, ctx);
  }

  // Accepted once ChatGPT empties the box, shows the new question, or starts answering.
  const accepted = questionsBefore => fresh(userMessages(), questionsBefore).length > 0 || generating() || boxLength(editor()) === 0;

  // Click send; if ChatGPT doesn't take it, press Enter, then submit the form, checking after each try.
  async function submit(step, questionsBefore) {
    const button = await waitFor(sendButton, 60000, '').catch(() => null);
    const tries = [
      [`clicking send ${describe(button ?? anySendButton())}`, () => (button ?? anySendButton())?.click()],
      ['pressing Enter', () => editor()?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true, cancelable: true }))],
      ['submitting the form', () => {
        const form = editor()?.form;
        form?.requestSubmit(form.querySelector(SEND) ?? undefined);
      }],
    ];
    for (const [what, act] of tries) {
      step(what);
      act();
      try {
        await waitFor(() => accepted(questionsBefore), 8000, '');
        step('sent');
        return;
      } catch {}
      if (!composer()) return; // the page is moving to the new chat; the copy there follows the answer
    }
    throw new Error('could not send');
  }

  // Whether a Phrasly humanizer tab is open; if so, ChatGPT's text isn't shown, only Phrasly's version of it.
  async function humanizerOpen() {
    try {
      return (await chrome.runtime.sendMessage({ type: 'humanizer' }))?.open === true;
    } catch {
      return false;
    }
  }

  async function streamAnswer(id, step, ctx = { known: new Set() }) {
    let text = '', sent = '', changedAt = Date.now(), sentAt = 0, sawBusy = false, described = false;
    const startedAt = Date.now();
    const humanizer = await humanizerOpen();
    if (humanizer) step('a Phrasly humanizer tab is open: the bar waits for its version');
    const deadline = Date.now() + 5 * 60 * 1000;
    try {
      for (;;) {
        await sleep(200);
        const answer = ourAnswer(ctx);
        const now = answer ? answerText(answer) : '';
        if (!now && !described && Date.now() - startedAt > 20000) {
          described = true;
          step(`the answer has no readable text after 20s; the page now:\n${describePage()}`);
        }
        if (now !== text) {
          text = now;
          changedAt = Date.now();
        }
        if (text && text !== sent && Date.now() - sentAt > 250) {
          if (!humanizer) {
            report({ id, text, done: false });
          } else if (!sent) {
            report({ id, stage: 'answering' }); // the bar only says ChatGPT is answering
          }
          sent = text;
          sentAt = Date.now();
        }
        const busy = generating();
        sawBusy ||= busy;
        // Without a recognisable stop button a pause in the answer could look like the end, so wait longer then.
        const finished = !busy && text && Date.now() - changedAt > (sawBusy ? 1200 : 4000);
        if (finished || Date.now() > deadline) break;
      }
      const complete = Date.now() <= deadline;
      step(complete ? `answer finished, ${text.length} characters` : 'stopped following the answer after 5 minutes');
      if (humanizer && complete && text) {
        send({ type: 'humanize', id, text }); // Phrasly's version goes to the bar, or this text if it can't humanize it
      } else {
        report({ id, text, done: true });
      }
    } finally {
      sessionStorage.removeItem(PENDING);
    }
  }

  function fail(id, e) {
    if (!current()) return;
    sessionStorage.removeItem(PENDING);
    let debug = '';
    try {
      debug = describePage();
    } catch (err) {
      debug = `could not describe the page: ${err}`;
    }
    report({ id, error: e.message || String(e), debug });
  }

  // After a page change mid-answer: give the answer a moment to start streaming, then follow it.
  async function resumePending() {
    let pending;
    try {
      pending = JSON.parse(sessionStorage.getItem(PENDING));
    } catch {}
    if (!pending) return;
    if (Date.now() - pending.at > 5 * 60 * 1000) {
      sessionStorage.removeItem(PENDING);
      return;
    }
    const step = stepper(pending.id);
    step(`page changed after sending; following the answer on ${where()}`);
    const ctx = { known: new Set(pending.known ?? []), questionsBefore: pending.questions ? new Set(pending.questions) : undefined };
    try {
      await waitFor(() => generating(), 15000, '').catch(() => {});
      await waitFor(() => ourAnswer(ctx), 4 * 60 * 1000, 'no answer');
      await streamAnswer(pending.id, step, ctx);
    } catch (e) {
      fail(pending.id, e);
    }
  }

  // Asks the extension's worker for the next snip; with several ChatGPT tabs it picks one of them.
  function nextShot() {
    return new Promise(resolve => {
      let port;
      try {
        port = chrome.runtime.connect({ name: 'wait' });
      } catch {
        resolve({ gone: true }); // extension reloaded; the new copy takes over after a page refresh
        return;
      }
      port.onMessage.addListener(msg => {
        port.disconnect();
        resolve(msg);
      });
      port.onDisconnect.addListener(() => resolve({ error: 'extension worker restarted' }));
    });
  }

  async function main() {
    log(`ready on ${where()}`);
    await resumePending();
    for (;;) {
      if (!current()) return;
      const asked = Date.now();
      const msg = await nextShot();
      if (msg.gone) return;
      if (!msg.shot) {
        // The app isn't running or the worker restarted; a quick empty reply is never repeated in a tight loop.
        if (msg.error || Date.now() - asked < 1000) await sleep(3000);
        continue;
      }
      const step = stepper(msg.shot.id);
      try {
        await handle(msg.shot, step);
      } catch (e) {
        fail(msg.shot.id, e);
      }
    }
  }

  main();
})();
