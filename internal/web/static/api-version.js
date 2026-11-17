// Every same-origin /api request states the API version this page speaks.
(() => {
  const version = '1';
  const send = window.fetch.bind(window);
  window.MitosAPI = {version, websocketProtocol: 'mitos-api.v' + version};
  window.fetch = (input, init = {}) => {
    const url = new URL(input instanceof Request ? input.url : String(input), location.href);
    if (url.origin === location.origin && (url.pathname === '/api' || url.pathname.startsWith('/api/'))) {
      const headers = new Headers(init.headers || (input instanceof Request ? input.headers : undefined));
      headers.set('Mitos-API-Version', version);
      init = {...init, headers};
    }
    return send(input, init);
  };
})();
