(() => {
 'use strict';
 let sequence = 0;
 class MitosInstructions extends HTMLElement {
  connectedCallback() {
   if (!this._built) this._build();
   this._events?.abort();
   this._events = new AbortController();
   const options = {signal:this._events.signal};
   window.addEventListener('resize', () => this.close(), options);
   document.addEventListener('scroll', event => {
    if (event.target instanceof Node && this._popup.contains(event.target)) return;
    const rect = this._trigger.getBoundingClientRect();
    // Focus can scroll the anchor before opening help. Ignore that queued scroll event.
    if (this._anchor && (rect.top !== this._anchor.top || rect.left !== this._anchor.left)) this.close();
   }, {...options, capture:true});
  }
  disconnectedCallback() { this._events?.abort(); clearTimeout(this._timer); this.close(); }
  static get observedAttributes() { return ['label', 'mode']; }
  attributeChangedCallback() { if (this._built) this._render(); }
  get value() { return this._value ?? this.textContent.trim(); }
  set value(markdown) { this._value = String(markdown ?? ''); if (this._built) this._render(); }
  _build() {
   this._value = this.value;
   this._built = true;
   this._trigger = document.createElement('button');
   this._trigger.type = 'button';
   this._trigger.className = 'instructions-trigger';
   this._trigger.textContent = '?';
   this._popup = document.createElement('div');
   this._popup.id = 'instructions-' + (++sequence);
   this._popup.className = 'instructions-popup';
   this._popup.setAttribute('popover', 'auto');
   this._heading = document.createElement('strong');
   this._close = document.createElement('button');
   this._close.type = 'button';
   this._close.className = 'instructions-close';
   this._close.textContent = '×';
   this._close.setAttribute('aria-label', 'Close instructions');
   this._close.onclick = () => { this.close(); this._trigger.focus({preventScroll:true}); };
   this._body = document.createElement('div');
   this._popup.append(this._heading, this._close, this._body);
   this.replaceChildren(this._trigger, this._popup);
   this._trigger.setAttribute('aria-controls', this._popup.id);
   this._trigger.onclick = () => {
    if (this._pinned) this.close();
    else { this._pinned = true; this.open(); }
   };
   this._trigger.addEventListener('pointerenter', event => {
    if (this._tooltip && event.pointerType === 'mouse') this.open();
   });
   this._trigger.addEventListener('focus', () => { if (this._tooltip) this.open(); });
   const hide = () => {
    clearTimeout(this._timer);
    this._timer = setTimeout(() => {
     if (!this._pinned && !this.matches(':hover') && !this.contains(document.activeElement)) this.close();
    }, 120);
   };
   this.addEventListener('pointerleave', hide);
   this.addEventListener('focusout', hide);
   this._popup.addEventListener('pointerenter', () => clearTimeout(this._timer));
   this._popup.addEventListener('toggle', event => {
    const open = event.newState === 'open';
    this._trigger.setAttribute('aria-expanded', String(open));
    if (open) this._position(); else this._pinned = false;
   });
   this._render();
  }
  _render() {
   this.close();
   const label = this.getAttribute('label') || 'Instructions';
   this._trigger.setAttribute('aria-label', label + ' help');
   this._trigger.setAttribute('aria-expanded', 'false');
   this._heading.textContent = label;
   // Use the same safe Markdown renderer as agent output and document previews.
   this._body.replaceChildren(window.MitosFormat.markdown(this.value, {link:(href, text) => {
    // Help may link to local screens, but never protocol-relative or executable URLs.
    if (!/^\/(?!\/)/.test(href) || href.includes('\\')) return;
    const link = document.createElement('a'); link.href = href; link.textContent = text; return link;
   }}));
   const mode = this.getAttribute('mode');
   this._tooltip = mode !== 'popover' && this.value.length <= 160 && !this._body.querySelector('a,ul,ol,pre,table');
   this._heading.hidden = this._close.hidden = this._tooltip;
   this._popup.setAttribute('role', this._tooltip ? 'tooltip' : 'region');
   this._popup.setAttribute('aria-label', label);
   if (this._tooltip) this._trigger.setAttribute('aria-describedby', this._popup.id);
   else this._trigger.removeAttribute('aria-describedby');
   // Older browsers retain readable instructions instead of losing help entirely.
   if (!this._popup.showPopover) { this._trigger.hidden = this._close.hidden = true; this._popup.removeAttribute('popover'); }
  }
  open() {
   clearTimeout(this._timer);
   if (!this.isConnected || !this._popup.showPopover) return;
   this._popup.showPopover();
   this._position();
  }
  close() { this._pinned = false; if (this._popup?.showPopover && this._popup.isConnected && this._popup.matches(':popover-open')) this._popup.hidePopover(); }
  _position() {
   const rect = this._trigger.getBoundingClientRect(), gap = 8;
   this._anchor = {top:rect.top, left:rect.left};
   const width = this._popup.offsetWidth, height = this._popup.offsetHeight;
   this._popup.style.left = Math.max(gap, Math.min(rect.left, innerWidth - width - gap)) + 'px';
   this._popup.style.top = Math.max(gap, Math.min(rect.bottom + gap, innerHeight - height - gap)) + 'px';
  }
 }
 customElements.define('mitos-instructions', MitosInstructions);
 window.MitosInstructions = {create(markdown, label = 'Instructions', mode = 'auto') {
  const element = document.createElement('mitos-instructions');
  element.setAttribute('label', label);
  element.setAttribute('mode', mode);
  element.value = markdown;
  return element;
 }};
})();
