// Copy buttons for the commands. Without this module a command is still plain text,
// selected whole by one click (user-select: all), and the buttons stay hidden
// (<noscript> in each page's head). With it, Copy puts the command on the clipboard,
// reads "Copied" for 1.6 s and says so in the page's polite live region.
// Stores nothing, sets no cookies.

const live = document.getElementById('copied');

for (const btn of document.querySelectorAll('button[data-copy]')) {
  let timer = 0;
  btn.addEventListener('click', async () => {
    const text = btn.dataset.copy;
    let ok = false;
    try {
      await navigator.clipboard.writeText(text);
      ok = true;
    } catch {
      ok = fallback(btn);
    }
    if (!ok) return;
    btn.textContent = 'Copied';
    if (live) live.textContent = 'Copied to the clipboard.';
    clearTimeout(timer);
    timer = setTimeout(() => {
      btn.textContent = 'Copy';
      if (live) live.textContent = '';
    }, 1600);
  });
}

// No clipboard API (an older browser, or a page not served over https): select the
// command so it can be copied by hand, and try the legacy copy command on it.
function fallback(btn) {
  const code = btn.parentElement && btn.parentElement.querySelector('code');
  if (!code) return false;
  const range = document.createRange();
  range.selectNodeContents(code);
  const sel = window.getSelection();
  sel.removeAllRanges();
  sel.addRange(range);
  try {
    return document.execCommand('copy');
  } catch {
    return false;
  }
}
