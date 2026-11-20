// Runs on chatgpt.com: takes each screenshot from Teleprompter, sends it with the prompt,
// and streams the answer text back to the bar as it is written.
const sleep = ms => new Promise(r => setTimeout(r, ms));

const editor = () => document.querySelector('#prompt-textarea');
const assistantMessages = () => document.querySelectorAll('[data-message-author-role="assistant"]');
const generating = () => !!document.querySelector('[data-testid="stop-button"]');
const composerImages = () => editor()?.closest('form')?.querySelectorAll('img').length ?? 0;

function sendButton() {
  const b = document.querySelector('[data-testid="send-button"], #composer-submit-button');
  return b && !b.disabled && b.getAttribute('aria-disabled') !== 'true' ? b : null;
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
  const input = document.querySelector('input[type="file"]');
  if (!input) return;
  const dt = new DataTransfer();
  dt.items.add(file);
  input.files = dt.files;
  input.dispatchEvent(new Event('change', { bubbles: true }));
}

function typeAtEnd(text) {
  const el = editor();
  el.focus();
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

  // Paste like a user would; if no thumbnail shows up, fall back to the attach button's file input.
  const imagesBefore = composerImages();
  pasteFile(file);
  try {
    await waitFor(() => composerImages() > imagesBefore, 4000, '');
  } catch {
    chooseFile(file);
  }
  await waitFor(sendButton, 30000, 'could not attach image');
  if (shot.prompt) typeAtEnd(shot.prompt);
  (await waitFor(sendButton, 10000, 'could not send')).click();

  await waitFor(() => assistantMessages().length > answersBefore, 90000, 'no answer');
  let text = '', sent = '', changedAt = Date.now(), sentAt = 0;
  const deadline = Date.now() + 5 * 60 * 1000;
  for (;;) {
    await sleep(200);
    const all = assistantMessages();
    const now = answerText(all[all.length - 1]);
    if (now !== text) {
      text = now;
      changedAt = Date.now();
    }
    if (text && text !== sent && Date.now() - sentAt > 250) {
      report({ id: shot.id, text, done: false });
      sent = text;
      sentAt = Date.now();
    }
    const finished = !generating() && text && Date.now() - changedAt > 1200;
    if (finished || Date.now() > deadline) break;
  }
  report({ id: shot.id, text, done: true });
}

async function main() {
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
      report({ id: shot.id, error: e.message || String(e) });
    }
  }
}

main();
