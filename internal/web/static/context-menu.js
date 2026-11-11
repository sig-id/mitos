(() => {
  'use strict';
  const menu = document.createElement('div');
  menu.id = 'desk-context-menu'; menu.className = 'desk-context-menu';
  menu.setAttribute('role', 'menu'); menu.tabIndex = -1; menu.hidden = true;
  document.body.append(menu);
  const status = document.createElement('span');
  status.className = 'sr-only'; status.setAttribute('role', 'status');
  document.body.append(status);
  let anchor, anchorBounds, restoreFocus, generation = 0;
  function close(restore = false) {
    generation++;
    menu.hidden = true;
    if (anchor?.matches('a')) anchor.setAttribute('aria-expanded', 'false');
    if (restore) restoreFocus?.()?.focus({preventScroll:true});
    anchor = null; restoreFocus = null;
  }
  function enabledItems() { return [...menu.querySelectorAll('[role=menuitem]')].filter(item => !item.disabled); }
  function position(x, y) {
    const bounds = menu.getBoundingClientRect(), margin = 8;
    menu.style.left = `${Math.max(margin, Math.min(x, innerWidth - bounds.width - margin))}px`;
    menu.style.top = `${Math.max(margin, Math.min(y, innerHeight - bounds.height - margin))}px`;
  }
  function render(items, label, x, y) {
    menu.replaceChildren(); menu.setAttribute('aria-label', label);
    const heading = document.createElement('div'); heading.className = 'context-menu-heading'; heading.textContent = label; menu.append(heading);
    for (const item of items) {
      if (item.separator) { const line = document.createElement('hr'); line.setAttribute('role','separator'); menu.append(line); continue; }
      const node = document.createElement(item.href ? 'a' : 'button');
      if (item.href) { node.href = item.href; if (item.newTab) { node.target = '_blank'; node.rel = 'noopener'; } }
      else { node.type = 'button'; node.disabled = !!item.disabled; }
      node.textContent = item.label; node.tabIndex = -1; node.setAttribute('role', 'menuitem');
      if (item.disabled) { node.setAttribute('aria-disabled','true'); node.title = item.reason || ''; }
      if (item.danger) node.className = 'context-menu-danger';
      node.addEventListener('click', () => {
        close(true);
        if (item.run) Promise.resolve().then(item.run).then(() => { if (item.notice) status.textContent = item.notice; }).catch(window.DeskContextMenu.onError);
      });
      menu.append(node);
    }
    position(x,y);
    (enabledItems()[0] || menu).focus({preventScroll:true});
  }
  window.DeskContextMenu = {
    onError: err => { status.textContent = err.message || String(err); },
    close,
    isOpen: () => !menu.hidden,
    async open(target, x, y, label, loadItems, restore) {
      close(); anchor = target; anchorBounds = target.getBoundingClientRect(); restoreFocus = restore; const current = generation;
      if (anchor.matches('a')) anchor.setAttribute('aria-expanded','true'); menu.hidden = false;
      render([{label:'Loading actions…',disabled:true}],label,x,y);
      try {
        const items = await loadItems();
        if (generation === current) render(items,label,x,y);
      } catch (err) {
        if (generation === current) { close(true); window.DeskContextMenu.onError(err); }
      }
    }
  };
  menu.addEventListener('keydown', event => {
    const items = enabledItems(), index = items.indexOf(document.activeElement);
    let next;
    if (event.key === 'ArrowDown') next = (index + 1) % items.length;
    else if (event.key === 'ArrowUp') next = (index - 1 + items.length) % items.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = items.length - 1;
    else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(true); return; }
    else if (event.key === 'Tab') { close(true); return; }
    else if (event.key === ' ' && document.activeElement?.matches('a[role=menuitem]')) { event.preventDefault(); document.activeElement.click(); return; }
    else if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
      next = items.findIndex((_, offset) => items[(index + offset + 1) % items.length]?.textContent.toLowerCase().startsWith(event.key.toLowerCase()));
      if (next >= 0) next = (index + next + 1) % items.length; else return;
    } else return;
    event.preventDefault(); items[next]?.focus({preventScroll:true});
  });
  document.addEventListener('pointerdown', event => { if (!menu.hidden && !menu.contains(event.target)) close(); });
  document.addEventListener('contextmenu', event => { if (!event.target.closest('.project-context-target[data-project-id],.session-link') && !menu.contains(event.target)) close(); });
  document.addEventListener('scroll', event => {
    if (!menu.hidden && (event.target === document || event.target.contains?.(anchor))) {
      const current=anchor?.getBoundingClientRect();
      // Focus can queue a scroll event before the keyboard menu opens. Only
      // dismiss if its anchor actually moved after opening.
      if (!anchor?.isConnected || !current || Math.abs(current.top-anchorBounds.top)>1 || Math.abs(current.left-anchorBounds.left)>1) close();
    }
  }, true);
  window.addEventListener('resize', () => close(true));
  window.addEventListener('blur', () => close());
  window.addEventListener('pagehide', () => close());
})();
