// Runs on chatgpt.com: takes each screenshot from Teleprompter, sends it with the prompt,
// and streams the answer text back to the bar as it is written.
// Selectors cover ChatGPT's current composer (data-mobile-composer-*) and the older one (#prompt-textarea).
const sleep = ms => new Promise(r => setTimeout(r, ms));
const PENDING = 'teleprompter-pending-answer';

const editor = () => document.querySelector('[data-mobile-composer-prompt], #prompt-textarea');
const composer = () => {
  const ed = editor();
  return ed?.form || ed?.closest('form') || null;
};
const assistantMessages = () =>
  document.querySelectorAll('[data-message-role="assistant"], [data-message-author-role="assistant"]');
const generating = () => !!document.querySelector(
  '[data-message-role="assistant"][data-message-streaming]:not([data-message-complete]), ' +
  'button[data-composer-submit][data-stop-generating], [data-testid="stop-button"]');

function sendButton() {
  const b = composer()?.querySelector('button[data-composer-submit]:not([data-stop-generating])')
    || document.querySelector('[data-testid="send-button"], #composer-submit-button');
  const ready = b && !b.disabled && !b.hidden && !b.hasAttribute('data-visually-disabled')
    && b.getAttribute('aria-disabled') !== 'true';
  return ready ? b : null;
}

// Changes once an image is attached: the composer's upload flag, its ready-attachments field, or a preview image.
function attachmentState() {
  const form = composer();
  if (!form) return '';
  return [
    form.hasAttribute('data-octane-native-image-upload-pending'),
    form.elements.namedItem('imageAttachments')?.value ?? '',
    form.querySelectorAll('img').length,
  ].join('|');
}

async function waitFor(check, ms, failure) {
  const end = Date.now() + ms;
  for (;;) {
    const v = check();
    if (v) return v;
    if (Date.now() > end) throw new Error(failure);
    await sleep(150);
  }
}

function report(answer) {
  chrome.runtime.sendMessage({ type: 'answer', answer }).catch(() => {});
}

function pasteFile(file) {
  const dt = new DataTransfer();
  dt.items.add(file);
  editor().focus();
  editor().dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
}

function chooseFile(file) {
  const input = document.querySelector('#octane-mobile-composer-files-input, input[type="file"][data-octane-native-image-picker-source="files"]')
    || composer()?.querySelector('input[type="file"]') || document.querySelector('input[type="file"]');
  if (!input) return;
  const dt = new DataTransfer();
  dt.items.add(file);
  input.files = dt.files;
  input.dispatchEvent(new Event('change', { bubbles: true }));
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

function answerText(msg) {
  return (msg.querySelector('.markdown') || msg).innerText.trim();
}

async function handle(shot) {
  await waitFor(editor, 15000, 'open a chat first');
  const answersBefore = assistantMessages().length;
  const bytes = Uint8Array.from(atob(shot.png), c => c.charCodeAt(0));
  const file = new File([bytes], `screenshot-${shot.id}.png`, { type: 'image/png' });

  // Paste like a user would; if nothing gets attached, use the attach button's file input instead.
  const before = attachmentState();
  pasteFile(file);
  try {
    await waitFor(() => attachmentState() !== before, 5000, '');
  } catch {
    chooseFile(file);
    await waitFor(() => attachmentState() !== before, 5000, 'could not attach image');
  }
  if (shot.prompt) typeAtEnd(shot.prompt);
  // The send button stays disabled until the upload finishes.
  const button = await waitFor(sendButton, 60000, 'could not send');
  // Sending may load a new page; the copy of this script there picks the answer up from this note.
  sessionStorage.setItem(PENDING, JSON.stringify({ id: shot.id, at: Date.now() }));
  button.click();
  await waitFor(() => assistantMessages().length > answersBefore, 90000, 'no answer');
  await streamAnswer(shot.id);
}

async function streamAnswer(id) {
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
    report({ id, text, done: true });
  } finally {
    sessionStorage.removeItem(PENDING);
  }
}

// After a page change mid-answer: give the answer a moment to start streaming, then follow it.
async function resumePending() {
  let pending;
  try {
    pending = JSON.parse(sessionStorage.getItem(PENDING));
  } catch {}
  if (!pending) return;
  if (Date.now() - pending.at > 3 * 60 * 1000) {
    sessionStorage.removeItem(PENDING);
    return;
  }
  try {
    await waitFor(() => generating(), 15000, '').catch(() => {});
    await waitFor(() => assistantMessages().length, 30000, 'no answer');
    await streamAnswer(pending.id);
  } catch (e) {
    sessionStorage.removeItem(PENDING);
    report({ id: pending.id, error: e.message || String(e) });
  }
}

async function main() {
  await resumePending();
  for (;;) {
    if (!chrome.runtime?.id) return; // extension was reloaded; the new copy takes over
    let res;
    try {
      res = await chrome.runtime.sendMessage({ type: 'wait' });
    } catch {
      res = null;
    }
    if (!res?.ok) {
      await sleep(3000);
      continue;
    }
    const shot = res.result;
    if (!shot) continue;
    try {
      await handle(shot);
    } catch (e) {
      sessionStorage.removeItem(PENDING);
      report({ id: shot.id, error: e.message || String(e) });
    }
  }
}

main();
