(() => {
  'use strict';
  const $=id=>document.getElementById(id),root=$('git-diff');
  const el=(tag,text,cls)=>{const node=document.createElement(tag);if(text!==undefined)node.textContent=text;if(cls)node.className=cls;return node;};
  function path(value){
    value=value.trim();if(!value.startsWith('"'))return value;
    const bytes=[],encoder=new TextEncoder(),escapes={a:7,b:8,t:9,n:10,v:11,f:12,r:13,'"':34,'\\':92};
    for(let i=1;i<value.length-1;){if(value[i]==='\\'){const octal=value.slice(i+1).match(/^[0-7]{1,3}/);if(octal){bytes.push(parseInt(octal[0],8));i+=octal[0].length+1;}else{bytes.push(escapes[value[i+1]]??value.charCodeAt(i+1));i+=2;}}else{const char=String.fromCodePoint(value.codePointAt(i));bytes.push(...encoder.encode(char));i+=char.length;}}
    return new TextDecoder().decode(new Uint8Array(bytes));
  }
  function filePath(value){const name=path(value);return name==='/dev/null'?'':name.replace(/^[ab]\//,'');}
  function parse(patch){
    const files=[];let file,hunk;
    for(const line of patch.split('\n')){
      if(line.startsWith('diff --git ')){
        const names=line.slice(11).match(/^("(?:\\.|[^"\\])*"|a\/.*?) ("(?:\\.|[^"\\])*"|b\/.*)$/);
        file={old:names?filePath(names[1]):'',name:names?filePath(names[2]):'Changed file',hunks:[],meta:[],added:0,deleted:0};files.push(file);hunk=null;continue;
      }
      if(!file)continue;
      const match=line.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$/);
      if(match){hunk={old:Number(match[1]),oldCount:Number(match[2]??1),next:Number(match[3]),nextCount:Number(match[4]??1),label:match[5].trim(),lines:[]};file.hunks.push(hunk);continue;}
      if(hunk){
        if(line.startsWith('\\ No newline at end of file')){const last=hunk.lines.at(-1);if(last)last.noNewline=true;continue;}
        if([' ','+','-'].includes(line[0])){hunk.lines.push({kind:line[0],text:line.slice(1)});if(line[0]==='+')file.added++;if(line[0]==='-')file.deleted++;}
        continue;
      }
      if(line.startsWith('--- '))file.old=filePath(line.slice(4));
      else if(line.startsWith('+++ '))file.name=filePath(line.slice(4));
      else if(line.startsWith('rename from '))file.old=path(line.slice(12));
      else if(line.startsWith('rename to '))file.name=path(line.slice(10));
      else if(/^(new file mode|deleted file mode|old mode|new mode|similarity index|rename from|rename to|Binary files|GIT binary patch)/.test(line))file.meta.push(line);
    }
    return files;
  }
  function language(file){
    const ext=(file.name||file.old).split('.').pop().toLowerCase();
    return ({go:'go',js:'javascript',mjs:'javascript',cjs:'javascript',jsx:'javascript',ts:'typescript',tsx:'typescript',json:'json',py:'python',sh:'bash',bash:'bash',html:'html',css:'css',rs:'rust',md:'markdown',yml:'yaml',yaml:'yaml'})[ext]||'text';
  }
  function changed(left,right){let start=0;while(start<left.length&&start<right.length&&left[start]===right[start])start++;let a=left.length,b=right.length;while(a>start&&b>start&&left[a-1]===right[b-1]){a--;b--;}return [[start,a],[start,b]];}
  function table(file,labels=['Before','After']){
    const table=el('table',undefined,'diff-table'),caption=el('caption',(file.name||file.old)+' – HEAD compared with working tree','sr-only');table.append(caption);
    const columns=el('colgroup');for(const cls of ['diff-number-column','diff-text-column','diff-number-column','diff-text-column'])columns.append(el('col',undefined,cls));table.append(columns);
    const head=el('thead'),header=el('tr');for(const text of labels){const th=el('th',text);th.colSpan=2;th.scope='colgroup';header.append(th);}head.append(header);table.append(head);const body=el('tbody');table.append(body);const lang=language(file);
    function cell(row,line,number,kind,range){
      const gutter=el('td',line?String(number):'',`diff-line-number ${kind}`),content=el('td',undefined,`diff-cell ${kind}${line?'':' diff-empty'}`);gutter.setAttribute('aria-label',line?`${kind==='removed'?'Removed':kind==='added'?'Added':'Context'} line ${number}`:'No corresponding line');
      if(line){const pre=el('code',undefined,'diff-source');pre.append(window.MitosFormat.highlight(line.text,lang,range));content.append(pre);if(line.noNewline)content.append(el('span','No final newline','diff-no-newline'));}
      row.append(gutter,content);
    }
    for(const hunk of file.hunks){
      const gap=el('tr',undefined,'diff-hunk'),label=el('td',`Lines ${hunk.old} → ${hunk.next}${hunk.label?' · '+hunk.label:''}`);label.colSpan=4;gap.append(label);body.append(gap);
      let old=hunk.old,next=hunk.next,index=0;
      while(index<hunk.lines.length){
        const line=hunk.lines[index];
        if(line.kind===' '){const row=el('tr');cell(row,line,old++,'context');cell(row,line,next++,'context');body.append(row);index++;continue;}
        const removed=[],added=[];while(index<hunk.lines.length&&hunk.lines[index].kind!==' '){const item=hunk.lines[index++];(item.kind==='-'?removed:added).push(item);}
        for(let i=0;i<Math.max(removed.length,added.length);i++){
          const left=removed[i],right=added[i],ranges=left&&right?changed(left.text,right.text):[];const row=el('tr');
          cell(row,left,left?old++:null,'removed',ranges[0]);cell(row,right,right?next++:null,'added',ranges[1]);body.append(row);
        }
      }
      if(old!==hunk.old+hunk.oldCount||next!==hunk.next+hunk.nextCount){const row=el('tr'),note=el('td','This hunk is incomplete. Refresh or inspect the full diff locally.','diff-warning');note.colSpan=4;row.append(note);body.append(row);}
    }
    return table;
  }
  let signature='',openFiles=new Map();
  window.MitosDiff={preview(patch){const box=el('div',undefined,'diff-files');for(const file of parse(patch)){const details=el('details',undefined,'diff-file');details.open=true;details.append(el('summary',file.name||file.old),table(file));box.append(details)}if(!box.children.length)box.append(el('p',patch.trim()||'No changes.'));return box;},render(resources){if(!root)return;
    const key=JSON.stringify([resources.diff,resources.git_status,resources.diff_truncated]);if(key===signature)return;signature=key;
    for(const node of root.querySelectorAll('.diff-file'))openFiles.set(node.dataset.path,node.open);
    const files=parse(resources.diff||''),fragment=document.createDocumentFragment();
    $('git-raw-diff').textContent=resources.diff||'No tracked changes.';
    const status=$('git-status');status.replaceChildren();const lines=(resources.git_status||'').trimEnd().split('\n').filter(Boolean);
    $('git-status-summary').textContent=lines.length?`File status · ${lines.length}`:'Working tree clean';
    for(const line of lines){const match=line.match(/^([ MADRCU?!]{2}) (.*)$/);const row=el('div',undefined,'git-status-row');if(match){row.append(el('span',match[1]==='??'?'Untracked':match[1].trim(),'git-status-kind'),el('code',path(match[2])));}else row.textContent=line;status.append(row);}
    if(lines.some(line=>line.startsWith('?? ')))status.append(el('p','Untracked files are listed here; they are not part of the HEAD comparison.','muted'));
    const added=files.reduce((n,f)=>n+f.added,0),deleted=files.reduce((n,f)=>n+f.deleted,0),summary=$('diff-summary');summary.replaceChildren(el('span',`${files.length} changed ${files.length===1?'file':'files'}`),el('span',`+${added}`,'diff-added-count'),el('span',`−${deleted}`,'diff-deleted-count'));
    if(resources.diff_truncated)fragment.append(el('p','Showing a partial diff (128 KiB limit).','diff-warning'));
    for(const [index,file] of files.entries()){
      const name=file.name||file.old,node=el('details',undefined,'diff-file');node.dataset.path=name;node.open=openFiles.has(name)?openFiles.get(name):index===0;
      const header=el('summary'),title=file.old&&file.name&&file.old!==file.name?`${file.old} → ${file.name}`:name;
      header.append(el('span',title,'diff-file-name'),el('span',`+${file.added}`,'diff-added-count'),el('span',`−${file.deleted}`,'diff-deleted-count'));node.append(header);
      const content=el('div',undefined,'diff-file-content');node.append(content);let rendered=false;
      const show=()=>{if(!node.open||rendered)return;rendered=true;if(file.meta.length){const meta=el('p',file.meta.join(' · '),'diff-file-meta');content.append(meta);}if(file.hunks.length)content.append(table(file));else content.append(el('p',file.meta.some(v=>v.startsWith('Binary'))?'Binary file changed.':'No text hunks in this change.','diff-file-meta'));};
      node.addEventListener('toggle',show);show();fragment.append(node);
    }
    if(!files.length)fragment.append(el('p',resources.diff?.trim()||'No tracked changes against HEAD.','diff-empty-state'));
    root.replaceChildren(fragment);
  }};
})();
