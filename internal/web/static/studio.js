document.addEventListener('htmx:sendError', () => {
  const notice = document.getElementById('network-error');
  if (notice) notice.hidden = false;
});
document.addEventListener('htmx:afterSwap', (event) => {
  if (event.detail.target.id === 'shell') {
    const heading = document.querySelector('main h1');
    if (heading) document.title = `${heading.textContent} · Mitos`;
    const main = document.getElementById('main');
    if (main) main.focus({ preventScroll: true });
  }
});
