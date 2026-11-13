(() => {
 'use strict';
 const node=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n};
 const render=window.MitosFormat.markdown;
 let sequence=0;
 class MitosMarkdown extends HTMLElement {
  constructor(){super();this._options={};this._mode='preview';this._baseline='';this._built=false}
  connectedCallback(){if(!this._built)this._mode=this.getAttribute('mode')||(this.hasAttribute('editable')?'edit':'preview');this._build();this.refresh();this._observeSize()}
  disconnectedCallback(){this._drag=null;this._resize?.disconnect();cancelAnimationFrame(this._frame);cancelAnimationFrame(this._layoutFrame)}
  get value(){return this._input?.value??this._value??''}
  set value(v){v=String(v??'');this._value=v;if(!this._built){this._build()}if(this._input.value!==v)this._input.value=v;this._baseline=v;this.refresh()}
  get mode(){return this._mode}
  set mode(v){this._build();this._mode=['edit','split','source'].includes(v)?v:'preview';if(!this.hasAttribute('editable')&&this._mode!=='preview')this._mode='source';this._show()}
  get dirty(){return this.value!==this._baseline}
  configure(text,options={}){this._options=options;this._rendered=undefined;if(options.editable)this.setAttribute('editable','');if(options.filename)this.setAttribute('filename',options.filename);if(options.compact??!options.filename)this.setAttribute('compact','');this.value=text;this.mode=options.mode||(options.editable?'edit':'preview');return this}
  adopt(input,options={}){this._input=input;this._value=input.value;this._baseline=input.value;this._options=options;this.setAttribute('editable','');this.setAttribute('filename',options.filename||input.dataset.markdown||'Markdown');this._build();this.mode='edit';return this}
  _build(){
   if(this._built)return;this._built=true;
   const existing=this._input||this.querySelector('textarea');const initial=existing?.value??this._value??this.textContent;
   this.replaceChildren();this._baseline=initial;
   this._compact=this.hasAttribute('compact')&&!this.hasAttribute('editable');
   if(this._compact){this._input=node('textarea');this._input.value=initial;this._preview=node('div',undefined,'md-preview');this.append(this._preview);this._renderPreview();return}
   this._toolbar=node('div',undefined,'md-toolbar');this._toolbar.setAttribute('role','group');this._toolbar.setAttribute('aria-label','Markdown view');
   this._name=node('span',this.getAttribute('filename')||'Markdown','md-filename');this._toolbar.append(this._name);
   this._modes=new Map();const editing=this.hasAttribute('editable');
   for(const [key,label]of editing?[['edit','Edit'],['preview','Preview'],['split','Split']]:[['preview','Preview'],['source','Source']]){const b=this._button(label,()=>{const start=this._input.selectionStart,end=this._input.selectionEnd;this.mode=key;if(key==='edit'||key==='split'){this._input.focus({preventScroll:true});this._input.setSelectionRange(start,end)}});b.dataset.mode=key;this._toolbar.append(b);this._modes.set(key,b)}
   this._format=node('div',undefined,'md-format');this._format.setAttribute('role','group');this._format.setAttribute('aria-label','Markdown formatting');
   const formats=[['B','Bold','bold'],['I','Italic','italic'],['H','Heading','heading'],['Link','Insert link','link'],['Code','Code block','code'],['List','Bullet list','list'],['☑','Checklist','task']];
   for(const [label,title,kind]of formats){const b=this._button(label,()=>this._formatSelection(kind));b.title=title;b.setAttribute('aria-label',title);b.addEventListener('mousedown',event=>event.preventDefault());this._format.append(b)}
   this._panes=node('div',undefined,'md-panes');this._source=node('div',undefined,'md-source');this._gutter=node('div',undefined,'md-gutter');this._gutter.setAttribute('aria-hidden','true');this._numbers=node('div',undefined,'md-line-numbers');this._gutter.append(this._numbers);this._measure=node('div',undefined,'md-measure');this._measure.setAttribute('aria-hidden','true');
   if(existing)this.style.setProperty('--md-editor-height',Math.max(140,Math.min(480,existing.rows*24+32))+'px');
   this._input=existing||node('textarea');this._input.value=initial;this._input.classList.add('md-input');this._input.wrap='soft';this._input.spellcheck=false;this._input.readOnly=!editing;
   if(!this._input.hasAttribute('aria-label')&&!this._input.id)this._input.setAttribute('aria-label',(this.getAttribute('filename')||'Markdown')+' source');
   this._input.id ||= 'markdown-source-'+(++sequence);this._input.setAttribute('aria-describedby',this._input.id+'-status');
   this._source.append(this._gutter,this._input,this._measure);this._preview=node('div',undefined,'md-preview');this._preview.tabIndex=0;this._preview.setAttribute('role','region');this._preview.setAttribute('aria-label','Markdown preview');this._panes.append(this._source,this._preview);
   this._footer=node('div',undefined,'md-status');this._footer.id=this._input.id+'-status';
   this._handle=node('div',undefined,'md-resize-handle');this._handle.tabIndex=0;this._handle.setAttribute('role','separator');this._handle.setAttribute('aria-label','Resize Markdown height');this._handle.setAttribute('aria-orientation','horizontal');this._handle.setAttribute('aria-valuemin','140');this._handle.title='Drag to resize, or use the Up and Down arrow keys';
   this._handle.addEventListener('pointerdown',event=>{
    if(event.button!==0||this._drag)return;event.preventDefault();this._handle.focus({preventScroll:true});
    this._drag={id:event.pointerId,y:event.clientY,height:this._paneHeight(),rows:this._mode==='split'&&this.classList.contains('md-narrow')?2:1};this._handle.setPointerCapture(event.pointerId);
   });
   this._handle.addEventListener('pointermove',event=>{const drag=this._drag;if(drag&&drag.id===event.pointerId)this._setHeight(drag.height+(event.clientY-drag.y)/drag.rows)});
   const endResize=event=>{if(this._drag?.id!==event.pointerId)return;this._drag=null;if(this._handle.hasPointerCapture(event.pointerId))this._handle.releasePointerCapture(event.pointerId)};
   for(const event of ['pointerup','pointercancel','lostpointercapture'])this._handle.addEventListener(event,endResize);
   this._handle.addEventListener('keydown',event=>{if(event.key!=='ArrowUp'&&event.key!=='ArrowDown')return;event.preventDefault();this._setHeight(this._paneHeight()+(event.key==='ArrowDown'?24:-24))});
   this.append(this._toolbar,this._format,this._panes,this._footer,this._handle);
   this._input.addEventListener('invalid',()=>{this.mode='edit'});
   this._input.addEventListener('input',()=>{this._updateStatus();cancelAnimationFrame(this._frame);this._frame=requestAnimationFrame(()=>this._renderPreview())});
   for(const event of ['click','keyup','select','focus'])this._input.addEventListener(event,()=>this._updateStatus());
   this._input.addEventListener('scroll',()=>{this._numbers.style.transform=`translateY(${-this._input.scrollTop}px)`});
   this._input.addEventListener('keydown',event=>{
    if(!(event.ctrlKey||event.metaKey)||event.altKey)return;
    const kind={b:'bold',i:'italic',k:'link'}[event.key.toLowerCase()];if(kind&&editing){event.preventDefault();this._formatSelection(kind)}
    if(event.key.toLowerCase()==='s'&&editing){const saved=this.dispatchEvent(new CustomEvent('markdown-save',{bubbles:true,cancelable:true,detail:{value:this.value}}));if(!saved)event.preventDefault()}
   });
   this._show();
  }
  _button(text,click){const b=node('button',text);b.type='button';b.addEventListener('click',click);return b}
  _paneHeight(){return (this._source.hidden?this._preview:this._source).getBoundingClientRect().height}
  _setHeight(height){height=Math.max(140,Math.round(height));this.style.setProperty('--md-editor-height',height+'px');this._handle.setAttribute('aria-valuenow',String(height))}
  _show(){
   if(this._compact){this._mode='preview';this._renderPreview();return}
   const preview=this._mode==='preview',source=this._mode==='source';this.dataset.mode=this._mode;
   this._source.hidden=preview;this._preview.hidden=!(preview||this._mode==='split');this._format.hidden=preview||source||!this.hasAttribute('editable');
   for(const [key,b]of this._modes)b.setAttribute('aria-pressed',String(key===this._mode));
   this._toolbar.hidden=this.hasAttribute('compact')&&!this.hasAttribute('editable');this._footer.hidden=this.hasAttribute('compact');
   this._renderPreview();this._updateStatus();
  }
  _renderPreview(){
   const value=this.value;if(this._rendered===value)return;this._rendered=value;
   this._preview.replaceChildren(render(value,this._options));
   this._options.decorate?.(this._preview);
  }
  refresh({saved=false}={}){if(!this._built)return;if(saved)this._baseline=this.value;this._renderPreview();this._updateStatus()}
  _updateStatus(){
   if(!this._footer)return;
   const value=this.value,lines=value.split('\n').length;this._scheduleGutter();
   const before=value.slice(0,this._input.selectionStart||0),line=before.split('\n').length,column=before.length-before.lastIndexOf('\n');
   this._footer.textContent=(this.hasAttribute('editable')?(this.dirty?'Unsaved changes':'Markdown')+' · Ln '+line+', Col '+column:'Read only')+' · '+lines+' '+(lines===1?'line':'lines');
  }
  _scheduleGutter(){cancelAnimationFrame(this._layoutFrame);this._layoutFrame=requestAnimationFrame(()=>this._layoutGutter())}
  _layoutGutter(){
   const width=this._input.clientWidth,value=this.value;if(!width||this._source.hidden||this._measuredWidth===width&&this._measuredValue===value)return;
   this._measuredWidth=width;this._measuredValue=value;this._measure.style.width=width+'px';
   this._measure.replaceChildren(...value.split('\n').map(line=>node('div',line||'\u200b')));
   const heights=[...this._measure.children].map(line=>line.getBoundingClientRect().height);
   this._numbers.replaceChildren(...heights.map((height,i)=>{const line=node('span',String(i+1));line.style.height=height+'px';return line}));
   this._numbers.style.transform=`translateY(${-this._input.scrollTop}px)`;
  }
  _formatSelection(kind){
   if(!this.hasAttribute('editable'))return;const t=this._input;let from=t.selectionStart,to=t.selectionEnd,selection=t.value.slice(from,to),replacement='',offset=0,length=selection.length;
   if(kind==='bold'||kind==='italic'||kind==='link'){const markers={bold:['**','**','text'],italic:['*','*','text'],link:['[','](https://)','link text']}[kind];selection ||= markers[2];replacement=markers[0]+selection+markers[1];offset=markers[0].length;length=selection.length}
   else if(kind==='code'){selection ||= 'code';replacement='```\n'+selection+'\n```';offset=4;length=selection.length}
   else{from=from===0?0:t.value.lastIndexOf('\n',from-1)+1;const end=t.value.indexOf('\n',to);to=end<0?t.value.length:end;const prefix={heading:'## ',list:'- ',task:'- [ ] '}[kind];replacement=t.value.slice(from,to).split('\n').map(line=>prefix+line).join('\n');length=replacement.length}
   t.focus({preventScroll:true});t.setSelectionRange(from,to);
   // Native insertion keeps browser undo/redo and IME behavior on the textarea.
   if(!document.execCommand('insertText',false,replacement)){t.setRangeText(replacement,from,to,'end');t.dispatchEvent(new Event('input',{bubbles:true}))}
   t.setSelectionRange(from+offset,from+offset+length);this.refresh();
  }
  _observeSize(){if(this._compact)return;this._resize?.disconnect();this._resize=new ResizeObserver(entries=>{this.classList.toggle('md-narrow',this.clientWidth<680);this._handle.setAttribute('aria-valuenow',String(Math.round(this._paneHeight())));this._scheduleGutter()});this._resize.observe(this);this._resize.observe(this._input)}
 }
 customElements.define('mitos-markdown',MitosMarkdown);
 const view=(text,options={})=>document.createElement('mitos-markdown').configure(String(text??''),options);
 function enhance(root=document){for(const input of root.querySelectorAll('textarea[data-markdown]')){if(input.closest('mitos-markdown'))continue;const parent=input.parentNode,next=input.nextSibling,editor=document.createElement('mitos-markdown');editor.adopt(input);parent.insertBefore(editor,next)}}
 const refresh=root=>{for(const editor of root.querySelectorAll('mitos-markdown'))editor.refresh({saved:true})};
 window.MitosMarkdown={view,editor:(text,options={})=>view(text,{...options,editable:true,compact:false}),enhance,refresh};
 enhance();new MutationObserver(records=>{for(const r of records)for(const n of r.addedNodes)if(n.nodeType===1&&n.isConnected&&!n.closest('mitos-markdown')){if(n.matches('textarea[data-markdown]'))enhance(n.parentElement);else enhance(n)}}).observe(document.body,{childList:true,subtree:true});
 document.addEventListener('reset',e=>queueMicrotask(()=>refresh(e.target)),true);
 document.addEventListener('toggle',e=>{if(e.target.tagName==='DIALOG'&&e.target.open)refresh(e.target)},true);
})();
