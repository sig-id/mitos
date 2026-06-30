// app.js — LiveView client bootstrap.
//
// phoenix.js exposes the global `Phoenix` (transport: Phoenix.Socket).
// phoenix_live_view.js exposes the global `LiveView` (LiveView.LiveSocket).
// Without this file, no LiveSocket is ever constructed and connected, so every
// phx-click / phx-* binding in the console is inert in a real browser.
(function () {
  var meta = document.querySelector("meta[name='csrf-token']");
  var csrfToken = meta ? meta.getAttribute("content") : null;

  var liveSocket = new LiveView.LiveSocket("/live", Phoenix.Socket, {
    params: { _csrf_token: csrfToken },
    hooks: {}
  });

  // Surface connection state on the document so CSS can show a live dot.
  liveSocket.socket.onOpen(function () { document.documentElement.setAttribute("data-phx-conn", "open"); });
  liveSocket.socket.onError(function () { document.documentElement.setAttribute("data-phx-conn", "error"); });

  liveSocket.connect();

  // Expose for console debugging.
  window.liveSocket = liveSocket;
})();
