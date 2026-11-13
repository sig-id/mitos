// Polling updates execution facts without discarding an operator's form or reading position.
(() => {
 let detailsState=[];
 document.addEventListener('htmx:sendError', () => {
  const notice=document.getElementById('network-error');if(notice)notice.hidden=false;
 });
 document.addEventListener('htmx:beforeRequest', event => {
  if(event.detail.elt?.id==='live'&&document.activeElement?.closest('form'))event.preventDefault();
 });
 document.addEventListener('htmx:beforeSwap', event => {
  if(event.detail.target?.id==='live')detailsState=[...document.querySelectorAll('#live details[id]')].map(n=>[n.id,n.open]);
 });
 document.addEventListener('htmx:afterSwap', event => {
  if(event.detail.target?.id==='live')for(const [id,open] of detailsState){const n=document.getElementById(id);if(n)n.open=open;}
  const notice=document.getElementById('network-error');if(notice)notice.hidden=true;
 });
})();
