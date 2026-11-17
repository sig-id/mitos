// Evaluation scripts call the Studio API like any client: every /api request
// states the API version. Import this module before making requests.
const send = globalThis.fetch;
globalThis.fetch = (input, init = {}) => {
  const url = new URL(input instanceof Request ? input.url : String(input), 'http://localhost');
  if (url.pathname === '/api' || url.pathname.startsWith('/api/')) {
    const headers = new Headers(init.headers || (input instanceof Request ? input.headers : undefined));
    headers.set('Mitos-API-Version', '1');
    init = {...init, headers};
  }
  return send(input, init);
};
