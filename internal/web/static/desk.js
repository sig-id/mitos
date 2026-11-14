(() => {
  'use strict';
  const project = document.body.dataset.project, session = document.body.dataset.session;
  const $ = id => document.getElementById(id);
  const error = err => { $('desk-error').textContent = err.message || String(err); $('desk-error').hidden = false; };
  const api = window.MitosConnection.request;
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
  form('project-form', async b => { const p = await api('/api/projects', b); location.href = `/projects/${p.id}`; });
  form('session-form', async b => { if(b.role==='orchestrator'){await window.MitosCoordination.startSetup(b);return;} const s = await api(projectAPI('sessions'), b); location.href = `/workspace?project=${project}&session=${s.id}`; });
  form('topic-form', async (b,f) => { b.paths = b.paths.split('\n').map(x=>x.trim()).filter(Boolean); await api(projectAPI('topics'), b); f.reset(); $('topic-dialog').close(); await topics(); });
  let agentCatalog = [], agentSelection = 0, selectedInstallation;
  function sessionMode(){
    const orchestrator=$('session-role').value==='orchestrator', v=selectedInstallation;
    $('session-dialog-title').textContent=orchestrator?'Set up the orchestrator':'Start an agent';
    $('session-prompt-label').textContent=orchestrator?'Work goal (optional during analysis)':'Work goal';
    $('session-form').querySelector('[type=submit]').textContent=orchestrator?'Analyze project':'Start agent';
    $('session-form').querySelector('[type=submit]').disabled=!v?.installed||(orchestrator?!v?.capabilities.project_context:(v?.kind==='agent'&&!v?.capabilities.initial_prompt));
    $('session-prompt').required=!orchestrator&&v?.kind==='agent';
    $('agent-prompt-help').textContent=orchestrator?(v?.capabilities.project_context?'First, this agent analyzes the project in a separate background conversation. Review and edit its proposed context before starting work.':'This agent has no background project-analysis adapter. Choose Codex, Claude or Pi.'):(v?.kind==='agent'?(v?.capabilities.initial_prompt?'A concrete first goal is required. Starting a session begins work on that goal.':'This adapter cannot accept a work goal. Choose an agent with initial prompt support.'):'Open this application in its terminal.');
  }
  $('session-role').addEventListener('change',sessionMode);
  async function inspectAgent() {
    const generation = ++agentSelection, id = $('session-agent').value;
    selectedInstallation=null;sessionMode();
    if (!id) return;
    const query = new URLSearchParams({profile:$('agent-profile').value}); if (project) query.set('project',project);
    const v = await api(`/api/agents/${id}?${query}`);
    if (generation !== agentSelection) return;
    $('agent-summary').textContent = v.installed ? `${v.name}${v.version ? ' '+v.version : ''} · ${v.executable}` : `${v.name} is not installed on the selected PATH.`;
    selectedInstallation=v;sessionMode();
    $('session-prompt').disabled = !v.capabilities.initial_prompt;
    const info = $('agent-setup-content'); info.replaceChildren();
    info.append(el('p','Uses its own configuration, skills, and memory. Credential values are never shown.','muted'));
    info.append(el('p',v.auth,'muted'));
    for (const e of v.environment) info.append(el('p',`${e.name}: ${e.present?'set':'not set'}${e.source!==e.name?' (from '+e.source+')':''}`,'agent-setting'));
    for (const r of v.resources) info.append(el('p',`${r.scope} ${r.kind} · ${r.status}\n${r.path}`,'agent-setting'));
    if (!v.resources.length) info.append(el('p','No adapter-specific setup locations are defined. Native configuration still applies.','muted'));
  }
  function chooseAgent() {
    const v = agentCatalog.find(a=>a.id===$('session-agent').value);
    $('agent-profile').replaceChildren(new Option('Configured default profile',''));
    for (const p of v?.profiles || []) $('agent-profile').append(new Option(p.name,p.id));
    $('agent-profile-field').hidden = !v?.profiles.length;
    if (v && !v.installed && v.profiles.length) $('agent-profile').value=v.profiles[0].id;
    localStorage.setItem('mitos:agent',$('session-agent').value);
    inspectAgent().catch(error);
  }
  async function discoverAgents(refresh=false) {
    if (refresh) await api('/api/agents/refresh',{});
    agentCatalog=await api('/api/agents'+(project?'?project='+encodeURIComponent(project):''));
    const previous=$('session-agent').value || localStorage.getItem('mitos:agent');
    $('session-agent').replaceChildren();
    for (const a of agentCatalog) {
      const option=new Option(`${a.name}${a.installed?'':a.profiles.length?' · configured profiles':' · not installed'}`,a.id);
      option.disabled=!a.installed && !a.profiles.length; $('session-agent').append(option);
    }
    const available=agentCatalog.filter(a=>a.installed||a.profiles.length);
    $('session-agent').value=available.some(a=>a.id===previous)?previous:available[0]?.id || '';
    chooseAgent();
  }
  $('session-agent').addEventListener('change',chooseAgent);
  $('agent-profile').addEventListener('change',()=>inspectAgent().catch(error));
  $('refresh-agents').addEventListener('click',async()=>{const b=$('refresh-agents');b.disabled=true;try{await discoverAgents(true);}catch(err){error(err);}finally{b.disabled=false;}});
  discoverAgents().catch(error);
  let continuationSource, continuationKind, continuationAgent, continuationGeneration = 0;
  let continuationKey = '', continuationPayload = '';
  function continuationFields() {
    const native = $('continuation-mode').value === 'native';
    $('continuation-native-field').hidden = !native;
    $('continuation-native').disabled = !native;
    $('continuation-native').required = native;
    $('continuation-context-fields').hidden = native;
    $('continuation-topic').disabled = native;
    $('continuation-notes').disabled = native;
    const available = continuationAgent?.installed && (native ? continuationAgent.capabilities[continuationKind] : continuationAgent.capabilities.initial_prompt);
    $('continuation-form').querySelector('[type=submit]').disabled = !available;
    $('continuation-availability').textContent = available ? (native ? 'Continues the saved native conversation in a new terminal.' : 'Starts a fresh conversation with a handoff document. The working directory is shared.') : 'This agent or profile cannot receive the selected context. Choose another target or context mode.';
  }
  async function inspectContinuation() {
    const generation = ++continuationGeneration;
    continuationAgent = null; continuationFields();
    const q = new URLSearchParams({project:continuationSource.project_id, profile:$('continuation-profile').value});
    const agent = await api(`/api/agents/${$('continuation-agent').value}?${q}`);
    if (generation !== continuationGeneration) return;
    continuationAgent = agent; continuationFields();
  }
  async function selectContinuationAgent() {
    const a = agentCatalog.find(v=>v.id===$('continuation-agent').value);
    $('continuation-profile').replaceChildren(new Option('Configured default profile',''));
    for (const p of a?.profiles || []) $('continuation-profile').append(new Option(p.name,p.id));
    if (continuationKind !== 'handover') $('continuation-profile').value=continuationSource.profile_id || '';
    await inspectContinuation();
  }
  async function openContinuation(kind, sourceID = session) {
    if ($('session-actions')) $('session-actions').open=false;
    continuationSource=await api(`/api/sessions/${sourceID}`);
    continuationKind=kind;
    const f=$('continuation-form'); f.reset(); f.querySelector('.form-error').textContent='';
    continuationKey=''; continuationPayload=''; continuationAgent=null;
    if (!agentCatalog.length) agentCatalog=await api('/api/agents?project='+encodeURIComponent(project));
    const sourceAgent=agentCatalog.find(a=>a.id===continuationSource.agent_id);
    const nativeAvailable=kind!=='handover' && !!sourceAgent?.capabilities[kind];
    $('continuation-mode').querySelector('[value=native]').disabled=!nativeAvailable;
    $('continuation-mode').value=nativeAvailable && continuationSource.native_session_id?'native':'context';
    $('continuation-native').value=continuationSource.native_session_id || '';
    $('continuation-native').readOnly=!!continuationSource.native_session_id;
    $('continuation-target').hidden=kind!=='handover';
    $('continuation-agent').replaceChildren();
    for(const a of agentCatalog){const o=new Option(a.name,a.id);o.disabled=kind==='handover' && (!a.capabilities.initial_prompt || (!a.installed&&!a.profiles.length));$('continuation-agent').append(o);}
    $('continuation-agent').value=continuationSource.agent_id;
    if(kind==='handover'){
      const target=agentCatalog.find(a=>a.id!==continuationSource.agent_id && a.capabilities.initial_prompt && (a.installed||a.profiles.length));
      if(target)$('continuation-agent').value=target.id;
    }
    const topics=await api(`/api/projects/${continuationSource.project_id}/topics`);
    $('continuation-topic').replaceChildren(new Option('No topic selected',''));
    for(const t of topics)$('continuation-topic').append(new Option(t.title,t.id));
    $('continuation-topic').value=continuationSource.assignment?.handoff_topic_id || '';
    const title={resume:'Resume session',fork:'Fork session',handover:'Hand over to another agent'}[kind];
    $('continuation-title').textContent=title;f.querySelector('[type=submit]').textContent={resume:'Resume',fork:'Create fork',handover:'Hand over'}[kind];
    $('continuation-description').textContent={resume:'Continue this stopped session. Its previous output stays available.',fork:'Branch the work into a new session. The source keeps running. Both use the same files.',handover:'Pass the context to the selected agent. The source stops after the replacement launches.'}[kind];
    continuationFields();$('continuation-dialog').showModal();
    await selectContinuationAgent();
  }
  document.querySelectorAll('[data-continuation]').forEach(b=>b.addEventListener('click',()=>openContinuation(b.dataset.continuation).catch(error)));
  $('continuation-agent').addEventListener('change',()=>selectContinuationAgent().catch(error));
  $('continuation-profile').addEventListener('change',()=>inspectContinuation().catch(error));
  $('continuation-mode').addEventListener('change',continuationFields);
  $('continuation-form').addEventListener('submit',async event=>{
    event.preventDefault();const f=event.currentTarget,button=f.querySelector('[type=submit]'),notice=f.querySelector('.form-error');
    button.disabled=true;notice.textContent='';
    const body=Object.fromEntries(new FormData(f)),serialized=JSON.stringify(body);
    if(serialized!==continuationPayload){continuationKey=crypto.randomUUID();continuationPayload=serialized;}
    body.key=continuationKey;
    try{
      const s=await api(`/api/sessions/${continuationSource.id}/${continuationKind}`,body);
      location.href=`/workspace?project=${s.project_id}&session=${s.id}`;
    }catch(err){notice.textContent=err.message;continuationFields();}
  });
  document.addEventListener('pointerdown',e=>{const menu=$('session-actions');if(menu&&!menu.contains(e.target))menu.open=false;});
  $('session-actions')?.addEventListener('keydown',e=>{if(e.key==='Escape'){$('session-actions').open=false;$('session-actions').querySelector('summary').focus();}});
  const contextMenu = window.DeskContextMenu;
  contextMenu.onError = error;
  function wireContextLink(link) {
    link.setAttribute('aria-haspopup','menu'); link.setAttribute('aria-controls','desk-context-menu');
    link.setAttribute('aria-expanded','false'); link.setAttribute('aria-keyshortcuts','Shift+F10');
  }
  function contextActions(link) {
    const href=link.href || `/workspace?project=${encodeURIComponent(link.dataset.projectId)}`;
    const items=[{label:'Open',href},{label:'Open in new tab',href,newTab:true},{separator:true}];
    if(link.dataset.projectId){
      const id=link.dataset.projectId;
      items.push({label:'New session…',run:()=>{if(id===project)$('session-dialog').showModal();else location.href=`/workspace?project=${encodeURIComponent(id)}&action=new-session`;}});
      items.push({label:'New task…',href:`/new?project=${encodeURIComponent(id)}`},{separator:true});
      items.push({label:'Copy project path',run:()=>navigator.clipboard.writeText(link.dataset.projectDirectory),notice:'Project path copied'});
      return items;
    }
    const id=link.dataset.sessionId;
    return api(`/api/sessions/${id}`).then(s=>{
      const settled=['stopped','exited','failed'].includes(s.status), continuing=['running','stopped','exited','failed'].includes(s.status);
      const change=async(action,body)=>{
        await api(`/api/sessions/${id}/${action}`,body);
        await sessions();
        if(action==='stop' && id===session) {
          // The output stream applies lifecycle updates to the selected terminal.
          $('session-actions').open=false;
        }
      };
      items.push({label:'Resume…',disabled:!settled,reason:'Stop this session before resuming it.',run:()=>openContinuation('resume',id)});
      items.push({label:'Fork…',disabled:!continuing,reason:'Wait for shutdown or settle interrupted processes first.',run:()=>openContinuation('fork',id)});
      items.push({label:'Hand over…',disabled:!continuing,reason:'Wait for shutdown or settle interrupted processes first.',run:()=>openContinuation('handover',id)},{separator:true});
      items.push({label:'Interrupt (Ctrl+C)',disabled:s.status!=='running',reason:'The session must be running.',run:()=>change('input',{key:crypto.randomUUID(),data:'\x03'})});
      items.push({label:['exited','failed'].includes(s.status)?'Close session':s.status==='stopped'?'Stopped':s.status==='stopping'?'Stopping…':'Stop session',disabled:!['running','starting','exited','failed'].includes(s.status),danger:true,reason:s.status==='interrupted'?'Inspect and settle the previous processes first.':'The session is not running.',run:()=>change('stop',{})},{separator:true});
      items.push({label:'Copy session ID',run:()=>navigator.clipboard.writeText(id),notice:'Session ID copied'});
      return items;
    });
  }
  function showContext(event, keyboard=false) {
    const link=event.target.closest('.project-context-target[data-project-id],.session-link[data-session-id]');
    if(!link)return;
    event.preventDefault();
    const rect=link.getBoundingClientRect();
    const pointer=!keyboard && Number.isFinite(event.clientX) && Number.isFinite(event.clientY);
    const x=pointer?event.clientX:rect.left+16,y=pointer?event.clientY:rect.bottom;
    const attribute=link.dataset.projectId?'data-project-id':'data-session-id',id=link.getAttribute(attribute);
    if($('session-actions'))$('session-actions').open=false;
    const restore=()=>link.isConnected?link:document.querySelector(`[${attribute}="${CSS.escape(id)}"]`);
    contextMenu.open(link,x,y,link.dataset.projectName||link.dataset.sessionName,()=>contextActions(link),restore);
  }
  document.querySelector('.project-rail').addEventListener('contextmenu',event=>showContext(event));
  const workspaceSwitcher=$('workspace-switcher');
  document.addEventListener('pointerdown',event=>{
    if(!workspaceSwitcher.contains(event.target) && !event.target.closest('#desk-context-menu'))workspaceSwitcher.open=false;
  });
  workspaceSwitcher.addEventListener('toggle',()=>{
    if(!workspaceSwitcher.open)return;
    const popover=workspaceSwitcher.querySelector('.workspace-switcher-popover'),rect=workspaceSwitcher.querySelector('summary').getBoundingClientRect();
    popover.style.left=`${Math.max(8,Math.min(rect.left,innerWidth-popover.offsetWidth-8))}px`;
    popover.style.top=`${rect.bottom+6}px`;
    popover.style.maxHeight=`${Math.max(60,innerHeight-rect.bottom-14)}px`;
  });
  workspaceSwitcher.addEventListener('keydown',event=>{
    if(event.key==='Escape'){event.preventDefault();workspaceSwitcher.open=false;workspaceSwitcher.querySelector('summary').focus();}
  });
  workspaceSwitcher.querySelector('[data-dialog]').addEventListener('click',()=>{workspaceSwitcher.open=false;});
  document.querySelector('.project-rail').addEventListener('keydown',event=>{
    if(event.key==='ContextMenu'||(event.shiftKey&&event.key==='F10'))showContext(event,true);
  });
  if(new URLSearchParams(location.search).get('action')==='new-session' && project){
    $('session-dialog').showModal();const url=new URL(location.href);url.searchParams.delete('action');history.replaceState(null,'',url);
  }
  // A page restored from the browser back/forward cache contains a disposed
  // terminal and closed output connection. Reattach once instead of showing frozen UI.
  window.addEventListener('pageshow',event=>{if(event.persisted)location.reload();});
  if (!project) return;

  let term, focusTerminal, offset = 0, alive = true, ready = false, inputChain = Promise.resolve(), inputBroken = false;
  if (session) {
    term = new Terminal({cols:Number($('terminal').dataset.cols),rows:Number($('terminal').dataset.rows),fontSize:15,fontFamily:'"Liberation Mono", Consolas, monospace',lineHeight:1.35,cursorBlink:!matchMedia('(prefers-reduced-motion: reduce)').matches,scrollback:10000,theme:{background:'#14171b',foreground:'#d8dee9',cursor:'#aac5ff',selectionBackground:'#334864'},allowProposedApi:false});
    const fit = new FitAddon.FitAddon(); term.loadAddon(fit); term.open($('terminal'));
    const keyButtons = [...document.querySelectorAll('[data-terminal-key]')];
    const shortcuts = $('terminal-shortcuts');
    let savedOutput = false, savedNeedsScroll = false;
    function updateInputControls() {
      const enabled = ready && !inputBroken;
      term.options.disableStdin = !enabled;
      [...keyButtons, $('interrupt')].forEach(b => b.disabled = !enabled);
      $('terminal-input-hint').textContent = savedOutput ? 'Saved output' : inputBroken ? 'Input disconnected · reload to reconnect' : !ready ? 'Terminal input unavailable' : 'Click terminal to type';
    }
    focusTerminal = () => { if (!$('view-terminal').hidden && ready && !inputBroken && !document.querySelector('dialog[open]') && !window.DeskContextMenu.isOpen()) term.focus(); };
    $('terminal').addEventListener('click', focusTerminal);
    document.addEventListener('pointerdown', event => { if (!shortcuts.contains(event.target)) shortcuts.open = false; });
    shortcuts.addEventListener('keydown', event => {
      if (event.key === 'Escape') { event.preventDefault(); shortcuts.open = false; focusTerminal(); }
    });
    keyButtons.forEach(button => button.addEventListener('click', async () => {
      const key = button.dataset.terminalKey;
      const arrows = {up:'A',down:'B',right:'C',left:'D'};
      const data = arrows[key] ? `\x1b${term.modes.applicationCursorKeysMode ? 'O' : '['}${arrows[key]}` : {enter:'\r',escape:'\x1b',tab:'\t','ctrl-c':'\x03','ctrl-d':'\x04','ctrl-z':'\x1a','ctrl-l':'\x0c'}[key];
      shortcuts.open = false;
      const sent = send(data); focusTerminal(); await sent;
    }));
    updateInputControls();
    function send(data) {
      // Preserve keystroke ordering; never retry an ambiguous write automatically.
      inputChain = inputChain.then(async () => {
        if (!ready || inputBroken) throw new Error('Terminal input is disconnected. Reopen the session before sending more input.');
        await api(`/api/sessions/${session}/input`, {key:crypto.randomUUID(),data});
      }).catch(err => { inputBroken = true; updateInputControls(); error(err); });
      return inputChain;
    }
    term.onData(data => { if (ready && !inputBroken) send(data); });
    let resizeTimer, restored = false;
    function resize() {
      clearTimeout(resizeTimer); resizeTimer = setTimeout(() => {
        if (!alive || !restored || $('view-terminal').hidden) return;
        if (savedOutput) {
          if (savedNeedsScroll) { const output=$('terminal').querySelector('.saved-terminal');output.scrollTop=output.scrollHeight;savedNeedsScroll=false; }
          return;
        }
        // Saved output must fit too. Only a live process needs a PTY resize.
        fit.fit();
        if (ready) api(`/api/sessions/${session}/resize`,{rows:term.rows,cols:term.cols}).catch(()=>{});
      },150);
    }
    function showSavedOutput() {
      if (savedOutput) return;
      savedOutput = true;
      shortcuts.hidden = true;
      // A finished TUI cannot redraw after a resize. Preserve the restored cells
      // as a wrapping transcript instead of reflowing cursor-addressed output.
      const output=document.createElement('pre');
      output.className='saved-terminal';output.tabIndex=0;
      output.setAttribute('aria-label','Saved terminal output');
      const palette=['#2e3440','#bf616a','#a3be8c','#ebcb8b','#81a1c1','#b48ead','#88c0d0','#e5e9f0','#4c566a','#bf616a','#a3be8c','#ebcb8b','#81a1c1','#b48ead','#8fbcbb','#eceff4'];
      const color=(value,rgb)=>{
        if(rgb)return '#'+value.toString(16).padStart(6,'0');
        if(value<16)return palette[value];
        if(value>=232){const c=8+(value-232)*10;return `rgb(${c},${c},${c})`;}
        const n=value-16,level=x=>x===0?0:55+x*40;
        return `rgb(${level(Math.floor(n/36))},${level(Math.floor(n/6)%6)},${level(n%6)})`;
      };
      const buffer=term.buffer.active;
      for(let row=0;row<buffer.length;row++){
        const line=buffer.getLine(row),text=line.translateToString(true);
        let group=null,lastStyle='',columnText='';
        const flush=()=>{if(columnText){group.textContent=columnText;output.append(group);columnText='';}};
        // Keep one row boundary per restored line, including blank rows and diagrams.
        let remaining=text.length;
        for(let col=0;col<line.length&&remaining>0;col++){
          const cell=line.getCell(col);if(!cell||cell.getWidth()===0)continue;
          const chars=cell.getChars()||' ';remaining-=chars.length;
          let fg=cell.isFgDefault()?'':color(cell.getFgColor(),cell.isFgRGB());
          let bg=cell.isBgDefault()?'':color(cell.getBgColor(),cell.isBgRGB());
          if(cell.isInverse())[fg,bg]=[bg||'#14171b',fg||'#d8dee9'];
          const style=[fg&&`color:${fg}`,bg&&`background-color:${bg}`,cell.isBold()&&'font-weight:700',cell.isItalic()&&'font-style:italic',cell.isUnderline()&&'text-decoration:underline',cell.isDim()&&'opacity:.7'].filter(Boolean).join(';');
          if(!group||style!==lastStyle){flush();group=document.createElement('span');group.style.cssText=style;lastStyle=style;}
          columnText+=chars;
        }
        flush();output.append(document.createTextNode('\n'));
      }
      term.element.style.display='none';
      $('terminal').append(output);
      $('terminal').setAttribute('aria-label','Saved session terminal');
      $('terminal-input-hint').textContent='Saved output';
      savedNeedsScroll=$('view-terminal').hidden;
      output.scrollTop=output.scrollHeight;
    }
    const terminalObserver = new ResizeObserver(resize);
    terminalObserver.observe($('terminal'));
    window.addEventListener('resize',resize);
    $('interrupt').addEventListener('click', () => send('\x03'));
    let stopPending = false;
    function updateSessionStatus(status) {
      if (!['running','starting','stopping'].includes(status)) stopPending = false;
      if (stopPending) status = 'stopping';
      $('session-status').textContent = status;
      $('session-status').dataset.status = status;
      document.querySelectorAll('[data-continuation]').forEach(b=>{
        b.disabled=b.dataset.continuation==='resume' ? !['stopped','exited','failed'].includes(status) : ['starting','stopping','interrupted'].includes(status);
      });
      const button = $('stop-session');
      button.disabled = !['running','starting','failed','exited'].includes(status);
      button.textContent = status === 'stopping' ? 'Stopping…' : status === 'stopped' ? 'Stopped' : ['failed','exited'].includes(status) ? 'Close session' : 'Stop session';
      button.title = status === 'interrupted' ? 'Inspect the previous processes, then confirm they stopped.' : ['failed','exited'].includes(status) ? 'The process has exited. Close this session and keep its output.' : '';
      $('settle-session').hidden = status !== 'interrupted';
      const notice = $('session-action-status');
      notice.textContent = {failed:'The process failed and is no longer running. Close session keeps its output.',exited:'The process has exited. Close session keeps its output.',stopping:'Stopping the session’s processes…',stopped:'Session stopped. Its output is still available.',interrupted:'The server lost track of this session. Inspect its previous processes, then confirm they stopped.'}[status] || '';
      notice.hidden = !notice.textContent;
      if (status !== 'running') ready = false;
      updateInputControls();
      return status;
    }
    $('stop-session').addEventListener('click', async () => {
      $('session-actions').open=false;
      stopPending = true;
      updateSessionStatus('stopping');
      $('connection-status').textContent = 'Stopping session…';
      try {
        await api(`/api/sessions/${session}/stop`,{});
        const stopped = await api(`/api/sessions/${session}`);
        updateSessionStatus(stopped.status);
        await sessions();
      } catch (err) {
        stopPending = false;
        error(err);
        try { updateSessionStatus((await api(`/api/sessions/${session}`)).status); } catch {}
      }
    });
    $('settle-session').addEventListener('click',()=>api(`/api/sessions/${session}/settle`,{confirmed_stopped:true}).then(()=>location.reload()).catch(error));
    let outputSocket, outputRetry, outputWatchdog;
    function output() {
      if (!alive) return;
      const url=new URL(`/api/sessions/${session}/stream?offset=${offset}`,location.href);
      url.protocol=location.protocol==='https:'?'wss:':'ws:';
      const socket=new WebSocket(url); outputSocket=socket;
      let processing=Promise.resolve(), finished=false;
      const watch=()=>{clearTimeout(outputWatchdog);outputWatchdog=setTimeout(()=>socket.close(),25000);};
      watch();
      socket.onmessage=event=>{
        watch();
        processing=processing.then(async()=>{
          if (!alive) return;
          if (typeof event.data!=='string' || event.data.length>128*1024) throw new Error('Terminal frame exceeds limit');
          const v=JSON.parse(event.data);
          if (v.heartbeat) return;
          const data=Uint8Array.from(atob(v.data || ''),c=>c.charCodeAt(0));
          if (v.next!==offset+data.length) throw new Error('Terminal output offset mismatch');
          if (data.length) await new Promise(resolve=>term.write(data,resolve));
          if (!alive) return;
          // Commit and acknowledge only rendered bytes. The server keeps one
          // frame in flight, bounding memory even while a tab is backgrounded.
          offset=v.next;
          const status=updateSessionStatus(v.status), running=status==='running';
          // Replay cursor-addressed output at its recorded size before fitting it.
          if (data.length<65536) restored=true;
          finished=!['running','starting','stopping'].includes(status) && data.length<65536;
          if (!ready && data.length<65536 && running && socket.readyState===WebSocket.OPEN) {
            ready=true; resize();
            if (document.activeElement===document.body && !document.querySelector('dialog[open]')) focusTerminal();
          }
          $('connection-status').textContent=status==='stopping'?'Stopping session…':inputBroken?'Terminal input disconnected':running?(ready?'Connected':'Restoring scrollback…'):`Session ${status}`;
          updateInputControls();
          if (finished) showSavedOutput();
          if (socket.readyState===WebSocket.OPEN) socket.send(JSON.stringify({next:offset}));
        }).catch(()=>socket.close());
      };
      socket.onclose=()=>{
        clearTimeout(outputWatchdog);
        if (alive) {ready=false;updateInputControls();}
        // Finish an already queued render before choosing a reconnect cursor.
        processing.finally(()=>{
          if (!alive || finished) return;
          $('connection-status').textContent='Disconnected · reconnecting output';
          outputRetry=setTimeout(output,1000);
        });
      };
      socket.onerror=()=>socket.close();
    }
    output(); window.addEventListener('pagehide',()=>{alive=false;clearTimeout(outputRetry);clearTimeout(outputWatchdog);clearTimeout(resizeTimer);terminalObserver.disconnect();window.removeEventListener('resize',resize);outputSocket?.close();term.dispose();});
  }
  document.querySelectorAll('[data-tab]').forEach(b=>b.addEventListener('click',()=>{
    document.querySelectorAll('[data-tab]').forEach(tab=>{ const active=tab===b;tab.classList.toggle('active',active);tab.setAttribute('aria-selected',String(active));$(`view-${tab.dataset.tab}`).hidden=!active; });
    document.body.dataset.view=b.dataset.tab;
    if (['changes','resources'].includes(b.dataset.tab)) inspect().catch(error); else if(b.dataset.tab==='terminal') { window.dispatchEvent(new Event('resize')); if (focusTerminal) focusTerminal(); }
  }));
  let topicID='', topicRevision=0, topicCursor=0, topicsSignature='', jobsSignature='';
  const initialView=new URLSearchParams(location.search).get('view');
  if(['changes','resources'].includes(initialView))document.querySelector(`[data-tab=${initialView}]`)?.click();
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
    window.MitosDiff.render(r);$('git-worktrees').textContent=r.worktrees;
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
    const focusedSession=document.activeElement?.closest('.session-link')?.dataset.sessionId;
    $('session-list').replaceChildren();
    for(const s of values.filter(s=>s.role!=='orchestrator')){const link=el('a',undefined,`session-link${s.id===session?' selected':''}`);link.href=`/workspace?project=${project}&session=${s.id}`;link.dataset.sessionId=s.id;link.dataset.sessionName=s.name;wireContextLink(link);const text=el('span',s.name);text.append(el('small',`${s.role} · ${s.continuation ? s.continuation.kind+' · ' : ''}${s.status}`));link.append(el('span',undefined,`state-dot ${s.status}`),text);$('session-list').append(link);}
    if(focusedSession)document.querySelector(`[data-session-id="${CSS.escape(focusedSession)}"]`)?.focus({preventScroll:true});
    const selected=values.find(s=>s.id===session);if(selected?.detail)$('connection-status').textContent=selected.detail;
    if (selected) $('session-name').textContent=selected.name;
  }
  async function refresh(){try{await Promise.all([topics(),jobs(),sessions()]);window.MitosConnection.recovered('workspace');}catch(err){window.MitosConnection.failed('workspace',err);}if(alive)setTimeout(refresh,3000);}
  refresh();
})();
