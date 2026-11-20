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
    const known = findAll('[data-mobile-composer-prompt], #prompt-textarea, textarea[name="prompt"]').find(visible);
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

  const assistantMessages = () => findAll('[data-message-role="assistant"], [data-message-author-role="assistant"]');
  const generating = () => findAll(
    '[data-message-role="assistant"][data-message-streaming]:not([data-message-complete]), ' +
    'button[data-composer-submit][data-stop-generating], [data-testid="stop-button"], button[aria-label^="Stop" i]').length > 0;

  function anySendButton() {
    const isStop = b => b.hasAttribute('data-stop-generating') || /^stop/i.test(b.getAttribute('aria-label') || '');
    return [...(composer() || document).querySelectorAll(SEND)].find(b => !isStop(b))
      || findAll('[data-testid="send-button"], #composer-submit-button')[0] || null;
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
      'data attributes: ' + Object.entries(names).sort((a, b) => b[1] - a[1]).slice(0, 120).map(([n, c]) => `${n}:${c}`).join(' '),
    ].join('\n');
  }

  // The composer's hidden attachment fields: fileAttachments and pendingFileUploads when logged in,
  // imageAttachments and pendingImageUploads when logged out.
  const attachmentFields = box => [...box.querySelectorAll('input[type="hidden"]')].filter(i => /attachment|upload/i.test(i.name));
  const filled = v => !!v && v !== '[]' && v !== '{}';
  const pendingFlags = box => [...box.attributes].filter(a => /upload-pending/.test(a.name)).map(a => a.name);

  // Changes as soon as an image is pasted: an upload flag, an attachment field, or a preview image.
  function attachmentState(forLog = false) {
    const box = composer();
    if (!box) return 'no composer';
    return [
      ...attachmentFields(box).map(i => `${i.name}=${forLog ? i.value.length : i.value}`),
      ...pendingFlags(box),
      `img=${box.querySelectorAll('img').length}`,
      `blob-img=${document.querySelectorAll('img[src^="blob:"]').length}`,
    ].join(' ');
  }

  // Still uploading: ChatGPT ignores a send until every pending upload is done.
  function uploading(box) {
    return pendingFlags(box).length > 0 || attachmentFields(box).some(i => /^pending/i.test(i.name) && filled(i.value));
  }
  const attachedAndUploaded = box =>
    !uploading(box) && attachmentFields(box).some(i => /attachments$/i.test(i.name) && filled(i.value));

  const userMessages = () => findAll('[data-message-role="user"], [data-message-author-role="user"]');

  async function waitFor(check, ms, failure) {
    const end = Date.now() + ms;
    for (;;) {
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

  const fileInput = () => findAll('#octane-mobile-composer-files-input, input[type="file"][data-octane-native-image-picker-source="files"]')[0]
    || composer()?.querySelector('input[type="file"]') || findAll('input[type="file"]')[0] || null;

  function chooseFile(input, file) {
    const dt = new DataTransfer();
    dt.items.add(file);
    input.files = dt.files;
    input.dispatchEvent(new Event('change', { bubbles: true }));
  }

  const boxLength = el => (!el ? '?' : (el instanceof HTMLTextAreaElement ? el.value : el.innerText).length);

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

  function answerText(msg) {
    return (msg.querySelector('[data-assistant-markdown], .markdown') || msg).innerText.trim();
  }

  function stepper(id) {
    const start = Date.now();
    return text => log(`snip #${id} +${((Date.now() - start) / 1000).toFixed(1)}s: ${text}`);
  }

  async function handle(shot, step) {
    step(`reached the ChatGPT tab ${where()}`);
    const box = await waitFor(editor, 15000, 'chat box not found');
    step(`chat box ${describe(box)} in ${describe(composer())}`);
    const answersBefore = assistantMessages().length;
    const bytes = Uint8Array.from(atob(shot.png), c => c.charCodeAt(0));
    const file = new File([bytes], `screenshot-${shot.id}.png`, { type: 'image/png' });

    // Paste like a user would. The file input is only a fallback for when the page shows no sign of the paste at all,
    // since attaching both ways would add the image twice.
    const before = attachmentState();
    step(`attachments before: ${attachmentState(true)}`);
    pasteFile(file);
    try {
      await waitFor(() => attachmentState() !== before, 8000, '');
      step(`image pasted (${attachmentState(true)})`);
    } catch {
      const input = fileInput();
      step(`no sign of the pasted image after 8s; trying file input ${describe(input)}`);
      if (input) chooseFile(input, file);
      await waitFor(() => attachmentState() !== before, 8000, 'could not attach image');
      step(`image attached through the file input (${attachmentState(true)})`);
    }
    const box2 = composer();
    if (box2 && attachmentFields(box2).length) {
      await waitFor(() => composer() && attachedAndUploaded(composer()), 2 * 60 * 1000, 'image upload did not finish');
      step(`upload finished (${attachmentState(true)})`);
    }
    if (shot.prompt) {
      typeAtEnd(shot.prompt);
      step(`prompt typed, the box now holds ${boxLength(editor())} characters`);
    }
    const usersBefore = userMessages().length;
    // Sending may load a new page; the copy of this script there picks the answer up from this note.
    sessionStorage.setItem(PENDING, JSON.stringify({ id: shot.id, at: Date.now() }));
    await submit(step, usersBefore);
    // In a background tab ChatGPT may only show the answer once it is complete, and thinking can take minutes.
    await waitFor(() => assistantMessages().length > answersBefore, 4 * 60 * 1000, 'no answer');
    step('answer started');
    await streamAnswer(shot.id, step);
  }

  // Accepted once ChatGPT empties the box, shows the question, or starts answering.
  const accepted = usersBefore => userMessages().length > usersBefore || generating() || boxLength(editor()) === 0;

  // Click send; if ChatGPT doesn't take it, press Enter, then submit the form, checking after each try.
  async function submit(step, usersBefore) {
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
        await waitFor(() => accepted(usersBefore), 6000, '');
        step('sent');
        return;
      } catch {}
      if (!composer()) return; // the page is moving to the new chat; the copy there follows the answer
    }
    throw new Error('could not send');
  }

  async function streamAnswer(id, step) {
    let text = '', sent = '', changedAt = Date.now(), sentAt = 0;
    const deadline = Date.now() + 5 * 60 * 1000;
    try {
      for (;;) {
        await sleep(200);
        const all = assistantMessages();
        const now = all.length ? answerText(all[all.length - 1]) : '';
        if (now !== text) {
          text = now;
          changedAt = Date.now();
        }
        if (text && text !== sent && Date.now() - sentAt > 250) {
          report({ id, text, done: false });
          sent = text;
          sentAt = Date.now();
        }
        const finished = !generating() && text && Date.now() - changedAt > 1200;
        if (finished || Date.now() > deadline) break;
      }
      step(Date.now() > deadline ? 'stopped following the answer after 5 minutes' : 'answer finished');
      report({ id, text, done: true });
    } finally {
      sessionStorage.removeItem(PENDING);
    }
  }

  function fail(id, e) {
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
    try {
      await waitFor(() => generating(), 15000, '').catch(() => {});
      await waitFor(() => assistantMessages().length, 4 * 60 * 1000, 'no answer');
      await streamAnswer(pending.id, step);
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
