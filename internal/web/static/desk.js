(() => {
  'use strict';
  const project = document.body.dataset.project, session = document.body.dataset.session;
  const $ = id => document.getElementById(id);
  const error = err => { $('desk-error').textContent = err.message || String(err); $('desk-error').hidden = false; };
  async function api(path, body) {
    const response = await fetch(path, body === undefined ? {cache:'no-store'} : {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)});
    const value = await response.json();
    if (!response.ok) throw new Error(value.error || `Request failed (${response.status})`);
    return value;
  }
  const projectAPI = suffix => `/api/projects/${project}/${suffix}`;
  function el(tag, text, cls) { const e = document.createElement(tag); if (text !== undefined) e.textContent = text; if (cls) e.className = cls; return e; }
  function action(text, fn) { const b = el('button', text); b.type = 'button'; b.addEventListener('click', () => Promise.resolve().then(fn).catch(error)); return b; }
  document.querySelectorAll('[data-dialog]').forEach(b => b.addEventListener('click', () => $(b.dataset.dialog).showModal()));
  document.querySelectorAll('[data-close]').forEach(b => b.addEventListener('click', () => b.closest('dialog').close()));
  function form(id, submit) {
    $(id).addEventListener('submit', async event => {
      event.preventDefault(); const f = event.currentTarget, button = f.querySelector('[type=submit]');
      const notice = f.querySelector('.form-error'); if (notice) notice.textContent = '';
      button.disabled = true;
      try { await submit(Object.fromEntries(new FormData(f)), f); }
      catch (err) { if (notice) notice.textContent = err.message; else error(err); }
      finally { button.disabled = false; }
    });
  }
  form('project-form', async b => { const p = await api('/api/projects', b); location.href = `/workspace?project=${p.id}`; });
  form('session-form', async b => { const s = await api(projectAPI('sessions'), b); location.href = `/workspace?project=${project}&session=${s.id}`; });
  form('topic-form', async (b,f) => { b.paths = b.paths.split('\n').map(x=>x.trim()).filter(Boolean); await api(projectAPI('topics'), b); f.reset(); $('topic-dialog').close(); await topics(); });
  if (!project) return;

  let term, offset = 0, alive = true, ready = false, inputChain = Promise.resolve(), inputBroken = false;
  if (session) {
    term = new Terminal({cols:Number($('terminal').dataset.cols),rows:Number($('terminal').dataset.rows),fontSize:13,fontFamily:'"Liberation Mono", Consolas, monospace',lineHeight:1.25,cursorBlink:!matchMedia('(prefers-reduced-motion: reduce)').matches,scrollback:10000,theme:{background:'#14171b',foreground:'#d8dee9',cursor:'#aac5ff',selectionBackground:'#334864'},allowProposedApi:false});
    const fit = new FitAddon.FitAddon(); term.loadAddon(fit); term.open($('terminal'));
    function send(data) {
      // Preserve keystroke ordering; never retry an ambiguous write automatically.
      inputChain = inputChain.then(async () => {
        if (!ready || inputBroken) throw new Error('Terminal input is disconnected. Reopen the session before sending more input.');
        await api(`/api/sessions/${session}/input`, {key:crypto.randomUUID(),data});
      }).catch(err => { inputBroken = true; error(err); });
      return inputChain;
    }
    term.onData(data => { if (ready && !inputBroken) send(data); });
    let resizeTimer;
    function resize() {
      clearTimeout(resizeTimer); resizeTimer = setTimeout(() => {
        if (!ready || $('view-terminal').hidden) return;
        fit.fit(); api(`/api/sessions/${session}/resize`,{rows:term.rows,cols:term.cols}).catch(()=>{});
      },150);
    }
    new ResizeObserver(resize).observe($('terminal'));
    const promptKey = `mitos:prompt:${session}`;
    $('session-input').value = sessionStorage.getItem(promptKey) || '';
    $('session-input').addEventListener('input', () => sessionStorage.setItem(promptKey,$('session-input').value));
    form('prompt-form', async () => {
      const value = $('session-input').value; if (!value.trim()) return;
      const bytes = new TextEncoder().encode(value+'\r'); if (bytes.length > 8192) throw new Error('Send at most 8192 bytes at a time.');
      if (!ready || inputBroken) throw new Error('Wait for the session to connect before sending input.');
      term.paste(value); await send('\r'); if (!inputBroken) { $('session-input').value=''; sessionStorage.removeItem(promptKey); }
    });
    $('interrupt').addEventListener('click', () => send('\x03'));
    $('stop-session').addEventListener('click', () => api(`/api/sessions/${session}/stop`,{}).catch(error));
    $('settle-session').addEventListener('click',()=>api(`/api/sessions/${session}/settle`,{confirmed_stopped:true}).then(()=>location.reload()).catch(error));
    async function output() {
      if (!alive) return;
      try {
        const v = await api(`/api/sessions/${session}/output?offset=${offset}`);
        const data = Uint8Array.from(atob(v.data || ''), c=>c.charCodeAt(0));
        if (data.length) await new Promise(resolve => term.write(data, resolve));
        offset = v.next; $('session-status').textContent = v.status;
        const running = v.status === 'running';
        if (!ready && data.length < 65536 && running) { ready=true; resize(); }
        $('connection-status').textContent = running ? (ready?'Connected · real terminal':'Restoring scrollback…') : `Session ${v.status}`;
        ['interrupt','stop-session'].forEach(id=>$(id).disabled=!running);
        $('prompt-form').querySelector('button').disabled=!running || !ready;
        if (!running) ready=false;
        setTimeout(output, data.length === 65536 ? 0 : running ? 180 : 1500);
      } catch(err) { $('connection-status').textContent='Disconnected · retrying output'; setTimeout(output,2000); }
    }
    output(); window.addEventListener('pagehide',()=>{alive=false;term.dispose();});
  }
  document.querySelectorAll('[data-tab]').forEach(b=>b.addEventListener('click',()=>{
    document.querySelectorAll('[data-tab]').forEach(tab=>{ const active=tab===b;tab.classList.toggle('active',active);tab.setAttribute('aria-selected',String(active));$(`view-${tab.dataset.tab}`).hidden=!active; });
    if (b.dataset.tab!=='terminal') inspect().catch(error); else window.dispatchEvent(new Event('resize'));
  }));
  let topicID='', topicRevision=0, topicCursor=0, topicsSignature='', jobsSignature='';
  async function topics() {
    const query=new URLSearchParams({q:$('topic-search').value}); if ($('followed-only').checked) query.set('follower','operator');
    const values=await api(projectAPI(`topics?${query}`)), signature=JSON.stringify(values); if (signature===topicsSignature) return; topicsSignature=signature;
    $('topic-list').replaceChildren();
    if (!values.length) $('topic-list').append(el('p','No matching topics. Publish a summary so agents can discover relevant work.','muted'));
    for (const t of values) {
      const card=el('article',undefined,'topic-card');card.append(action(t.title,()=>openTopic(t.id)),el('p',t.summary),el('span',`revision ${t.revision}${(t.paths||[]).length?' · '+t.paths.join(', '):''}`,'topic-meta'));$('topic-list').append(card);
    }
  }
  let searchTimer;
  $('topic-search').addEventListener('input',()=>{clearTimeout(searchTimer);searchTimer=setTimeout(()=>topics().catch(error),250);});
  $('followed-only').addEventListener('change',()=>topics().catch(error));
  function appendPosts(t) {
    for (const p of t.posts || []) { const post=el('article',undefined,'thread-post');post.append(el('small',`${p.author} · ${new Date(p.at).toLocaleString()} · #${p.sequence}`),el('p',p.body));$('thread-posts').append(post);topicCursor=p.sequence; }
    $('thread-more').hidden=topicCursor>=t.revision;
  }
  async function openTopic(id) {
    const t=await api(`/api/topics/${id}`);topicID=id;topicRevision=t.revision;topicCursor=0;
    $('thread-title').textContent=t.title;$('thread-revision').textContent=`Revision ${t.revision}`;$('thread-posts').replaceChildren();appendPosts(t);
    $('reply-body').value=sessionStorage.getItem(`mitos:reply:${id}`)||'';$('reply-form').querySelector('.form-error').textContent='';$('follow-topic').textContent='Follow topic';
    if (!$('thread-dialog').open) $('thread-dialog').showModal();
  }
  $('thread-more').addEventListener('click',async()=>{try{appendPosts(await api(`/api/topics/${topicID}?after=${topicCursor}`));}catch(err){error(err);}});
  $('reply-body').addEventListener('input',()=>sessionStorage.setItem(`mitos:reply:${topicID}`,$('reply-body').value));
  form('reply-form',async()=>{await api(`/api/topics/${topicID}/posts`,{body:$('reply-body').value,revision:topicRevision});sessionStorage.removeItem(`mitos:reply:${topicID}`);await openTopic(topicID);await topics();});
  $('follow-topic').addEventListener('click',()=>api(`/api/topics/${topicID}/follow`,{session:'operator',cursor:topicCursor}).then(()=>{$('follow-topic').textContent='Following · read cursor saved';return topics();}).catch(error));
  async function jobs() {
    const values=await api(projectAPI('jobs')), signature=JSON.stringify(values);if(signature===jobsSignature)return;jobsSignature=signature;$('job-list').replaceChildren();
    if(!values.length)$('job-list').append(el('p','No managed builds yet.','muted'));
    for(const j of values.slice(-12).reverse()){
      const card=el('article',undefined,'job-card');card.append(el('code',j.command.join(' ')),el('p',`${j.status} · ${j.consumers.length} consumer(s)`));
      const controls=el('div',undefined,'job-actions');controls.append(action('Output',()=>openJob(j.id)));
      if(['running','queued'].includes(j.status)){
        if(!j.consumers.includes('operator'))controls.append(action('Join output',async()=>{await api(`/api/jobs/${j.id}/join`,{});await jobs();}));
        else controls.append(action('Release my request',async()=>{await api(`/api/jobs/${j.id}/release`,{});await jobs();}));
      }
      if(j.status==='interrupted')controls.append(action('Confirm previous process stopped',async()=>{await api(`/api/jobs/${j.id}/release`,{confirmed_stopped:true});await jobs();}));
      card.append(controls);$('job-list').append(card);
    }
  }
  $('build').addEventListener('click',async()=>{const b=$('build');b.disabled=true;try{await api(projectAPI('jobs'),{args:[]});await jobs();}catch(err){error(err);}finally{b.disabled=false;}});
  let jobTimer,jobGeneration=0;
  async function openJob(id){
    clearTimeout(jobTimer);const generation=++jobGeneration;let pos=0;const decoder=new TextDecoder();$('job-output').textContent='';if(!$('job-dialog').open)$('job-dialog').showModal();
    async function read(){if(!$('job-dialog').open||generation!==jobGeneration)return;try{const v=await api(`/api/jobs/${id}/output?offset=${pos}`);if(generation!==jobGeneration)return;const b=Uint8Array.from(atob(v.data||''),c=>c.charCodeAt(0));$('job-output').append(document.createTextNode(decoder.decode(b,{stream:true})));pos=v.next;if(b.length===65536||['running','queued'].includes(v.status))jobTimer=setTimeout(read,400);}catch(err){error(err);}}
    read();
  }
  $('job-dialog').addEventListener('close',()=>{jobGeneration++;clearTimeout(jobTimer);});
  const size=n=>n>1048576?`${(n/1048576).toFixed(1)} MiB`:`${(n/1024).toFixed(1)} KiB`;
  async function inspect(){
    const r=await api(projectAPI('resources?details=true'));
    $('git-status').textContent=r.git_status||'Working directory is clean.';$('git-diff').textContent=r.diff||'No tracked diff against HEAD. Untracked files appear in status above.';$('git-worktrees').textContent=r.worktrees;
    $('resource-coverage').textContent=r.coverage;$('resource-summary').replaceChildren();
    for(const [value,label] of [[r.processes.length,'observed processes'],[size(r.processes.reduce((n,p)=>n+p.rss_bytes,0)),'summed RSS · may share pages'],[size(r.disk_bytes),r.disk_partial?'allocated bytes · partial scan':'allocated bytes · directory scan']]){const stat=el('div',undefined,'resource-stat');stat.append(el('strong',String(value)),el('span',label));$('resource-summary').append(stat);}
    $('process-list').replaceChildren();
    for(const p of r.processes){const row=el('article',undefined,'process-row');row.append(el('strong',`${p.pid}  ${p.name}`),el('p',`${p.session_id} · ${size(p.rss_bytes)} RSS · ${p.cpu_seconds == null ? 'CPU time unavailable' : p.cpu_seconds.toFixed(2)+'s CPU time'} · ${p.open_count} handles`));if(p.open_files?.length){const d=el('details');d.append(el('summary','Observed workspace handles'),el('pre',p.open_files.join('\n')));row.append(d);}$('process-list').append(row);}
    if(!r.processes.length)$('process-list').append(el('p','No live project processes observed.','muted'));
    $('process-list').append(el('h2','Session scratch','activity-heading'));
    for(const s of r.scratch||[]){const row=el('article',undefined,'scratch-row');row.append(el('code',s.session_id),el('p',s.path),el('p',`${size(s.bytes)}${s.partial?' · partial scan':''}`));if(s.hold)row.append(el('p',`Cleanup deferred: ${s.hold}`));else row.append(action('Remove session scratch',async()=>{await api(`/api/sessions/${s.session_id}/scratch/clean`,{});await inspect();}));$('process-list').append(row);}
    $('process-list').append(el('h2','Observed Git commands','activity-heading'),el('p','Git Trace2 command names by session. This does not establish line authorship; programs can disable tracing.','muted'));
    for(const g of r.git_activity||[]){const row=el('article',undefined,'process-row');row.append(el('strong',`git ${g.command}`),el('p',`${g.session_id} · ${g.at}`));$('process-list').append(row);}
  }
  $('refresh-changes').addEventListener('click',()=>inspect().catch(error));$('refresh-resources').addEventListener('click',()=>inspect().catch(error));
  let sessionsSignature='';
  async function sessions(){
    const values=await api(projectAPI('sessions')),signature=JSON.stringify(values);if(signature===sessionsSignature)return;sessionsSignature=signature;
    $('session-list').replaceChildren();
    for(const s of values){const link=el('a',undefined,`session-link${s.id===session?' selected':''}`);link.href=`/workspace?project=${project}&session=${s.id}`;const text=el('span',s.name);text.append(el('small',`${s.role} · ${s.status}`));link.append(el('span',undefined,`state-dot ${s.status}`),text);$('session-list').append(link);}
    const selected=values.find(s=>s.id===session);if(selected?.detail)$('connection-status').textContent=selected.detail;
  }
  async function refresh(){try{await Promise.all([topics(),jobs(),sessions()]);}catch(err){error(err);}if(alive)setTimeout(refresh,3000);}
  refresh();
})();
