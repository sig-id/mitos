// Every same-origin /api request states the API version this page speaks.
(() => {
  const version = '1';
  const send = window.fetch.bind(window);
  const isAPI = url => url.origin === location.origin && (url.pathname === '/api' || url.pathname.startsWith('/api/'));
  window.MitosAPI = {version, websocketProtocol: 'mitos-api.v' + version};
  window.fetch = (input, init = {}) => {
    const url = new URL(input instanceof Request ? input.url : String(input), location.href);
    if (isAPI(url)) {
      const headers = new Headers(init.headers || (input instanceof Request ? input.headers : undefined));
      headers.set('Mitos-API-Version', version);
      init = {...init, headers};
    }
    return send(input, init);
  };
  // Plain navigation cannot carry the version header, so API links are fetched
  // with it and saved as a download.
  document.addEventListener('click', async event => {
    const link = event.target instanceof Element && event.target.closest('a[href]');
    if (!link || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const url = new URL(link.getAttribute('href'), location.href);
    if (!isAPI(url)) return;
    event.preventDefault();
    try {
      const response = await window.fetch(url);
      if (!response.ok) throw new Error(`${response.status} ${await response.text()}`);
      const blob = await response.blob(), object = URL.createObjectURL(blob);
      const save = document.createElement('a');
      save.href = object;
      save.download = link.getAttribute('download') || url.pathname.split('/').filter(Boolean).slice(-2).join('-') + '.json';
      document.body.append(save); save.click(); save.remove();
      setTimeout(() => URL.revokeObjectURL(object), 10000);
    } catch (error) {
      alert('Could not download ' + url.pathname + ': ' + error.message);
    }
  });
})();
