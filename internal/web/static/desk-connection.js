(() => {
  'use strict';
  const failures = new Map();
  function render() {
    const banner = document.getElementById('desk-connection');
    banner.hidden = failures.size === 0;
    if (banner.hidden) { banner.textContent = ''; return; }
    const errors = [...failures.values()];
    banner.textContent = errors.some(e => e.connectionFailure)
      ? 'Connection to Studio interrupted. Retrying automatically…'
      : `Could not refresh workspace data: ${errors[0].message}. Retrying automatically…`;
  }
  window.MitosConnection = {
    failed(source, error) { failures.set(source, error); render(); },
    recovered(source) { failures.delete(source); render(); },
    async request(path, body) {
      // Bound stalled reads so the next refresh can recover. Mutations are
      // submitted once and never automatically retried.
      const controller = new AbortController();
      const timer = body === undefined ? setTimeout(() => controller.abort(), 15000) : null;
      try {
        const response = await fetch(path, body === undefined
          ? {cache: 'no-store', signal: controller.signal}
          : {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
        if (!response.ok) {
          let message = `Request failed (${response.status})`;
          try { message = (await response.json()).error || message; } catch {}
          throw new Error(message);
        }
        return await response.json();
      } catch (error) {
        if (error instanceof TypeError || error.name === 'AbortError') {
          const failure = new Error(body === undefined
            ? 'Cannot reach Studio. The next refresh will retry.'
            : 'Connection to Studio interrupted. Check whether the action completed before retrying.');
          failure.connectionFailure = true;
          throw failure;
        }
        throw error;
      } finally {
        clearTimeout(timer);
      }
    }
  };
})();
