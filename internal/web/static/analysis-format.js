(() => {
  'use strict';
  const el=(tag,text,cls)=>{const node=document.createElement(tag);if(text!==undefined)node.textContent=text;if(cls)node.className=cls;return node;};
  function inline(text,options={}){
    const out=document.createDocumentFragment();
    for(const token of text.split(/(`[^`\n]+`|\*\*[^*\n]+\*\*|\*[^*\n]+\*|\[[^\]\n]+\]\([^\s)]+\))/g)){
      const link=token.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
      const resolved=link&&options.link?.(link[2],link[1]);
      if(resolved){out.append(resolved);continue;}
      if(link&&/^https?:\/\//i.test(link[2])){const a=el('a',link[1]);a.href=link[2];a.target='_blank';a.rel='noopener noreferrer';out.append(a);}
      else if(token.startsWith('`')&&token.endsWith('`'))out.append(el('code',token.slice(1,-1)));
      else if(token.startsWith('**')&&token.endsWith('**'))out.append(el('strong',token.slice(2,-2)));
      else if(token.startsWith('*')&&token.endsWith('*')&&token.length>2)out.append(el('em',token.slice(1,-1)));
      else out.append(document.createTextNode(token));
    }
    return out;
  }
  function language(text){
    if(/^\s*(package\s+\w+|func\s+|type\s+\w+\s+(?:struct|interface))/m.test(text))return 'go';
    if(/^\s*(?:from\s+\w+\s+import|def\s+\w+|class\s+\w+.*:)/m.test(text))return 'python';
    if(/^\s*(?:const |let |(?:export )?(?:async )?function |import .* from )/m.test(text))return 'javascript';
    if(/^\s*[\[{]/.test(text)){try{JSON.parse(text);return 'json';}catch{}}
    if(/^\s*(?:<!doctype|<html|<div|<script)/im.test(text))return 'html';
    if(/^#!.*(?:bash|sh)|^\s*(?:set -[a-z]+|export \w+=)/m.test(text))return 'bash';
    if(/^diff --git |^@@ /m.test(text))return 'diff';
    return 'text';
  }
  function code(text,hint){
    let value=text.trim(),lang=hint||language(value);if(lang==='json'){try{value=JSON.stringify(JSON.parse(value),null,2);}catch{}}
    const box=el('div',undefined,'analysis-code-block');box.append(el('div',lang,'analysis-code-language'));
    const pre=el('pre',undefined,'analysis-code'),body=el('code');pre.tabIndex=0;pre.append(body);box.append(pre);
    body.append(highlight(value,lang));return box;
  }
  function highlight(value,lang,range){
    const body=document.createDocumentFragment();
    const append=(text,offset,cls='')=>{
      const node=cls?el('span',undefined,cls):document.createDocumentFragment();
      const from=range?Math.max(0,range[0]-offset):text.length,to=range?Math.min(text.length,range[1]-offset):0;
      if(from<to){node.append(document.createTextNode(text.slice(0,from)),el('mark',text.slice(from,to),'diff-word'),document.createTextNode(text.slice(to)));}
      else node.append(document.createTextNode(text));
      body.append(node);
    };
    const tokens=/(\/\/[^\n]*|\/\*[\s\S]*?\*\/|#[^\n]*|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`[^`]*`|\b\d+(?:\.\d+)?\b|\b(?:package|import|from|func|function|async|await|return|var|let|const|type|struct|interface|class|def|if|else|for|while|switch|case|select|break|continue|defer|go|range|try|catch|throw|new|export|default|true|false|null|nil|None|True|False)\b)/g;
    let start=0;for(const match of value.matchAll(tokens)){
      append(value.slice(start,match.index),start);const token=match[0];
      const comment=token.startsWith('//')||token.startsWith('/*')||(token.startsWith('#')&&['python','bash','sh'].includes(lang));
      const cls=comment?'syntax-comment':/^["'`]/.test(token)?'syntax-string':/^\d/.test(token)?'syntax-number':/^#/.test(token)?'':'syntax-keyword';
      append(token,match.index,cls);start=match.index+token.length;
    }
    append(value.slice(start),start);return body;
  }
  function markdown(text,options={}){
    const box=el('div',undefined,'analysis-prose'),lines=text.trim().split('\n');let i=0;
    const append=(tag,text)=>{const node=el(tag);node.append(inline(text,options));box.append(node);return node;};
    while(i<lines.length){
      const line=lines[i];if(!line.trim()){i++;continue;}
      const fence=line.match(/^\s*(`{3,}|~{3,})([\w+-]*)/);
      if(fence){const content=[];i++;while(i<lines.length&&!lines[i].trim().startsWith(fence[1]))content.push(lines[i++]);if(i<lines.length)i++;box.append(code(content.join('\n'),fence[2]||undefined));continue;}
      if(/^\s*<!--/.test(line)){while(i<lines.length&&!lines[i].includes('-->'))i++;i++;continue;}
      const heading=line.match(/^\s{0,3}(#{1,6})\s+(.+)$/);
      if(heading){append('h'+Math.min(heading[1].length+2,6),heading[2]);i++;continue;}
      if(/^\s*(?:---+|\*\*\*+)\s*$/.test(line)){box.append(el('hr'));i++;continue;}
      if(line.includes('|')&&/^\s*\|?\s*:?-{3,}/.test(lines[i+1]||'')){
        const table=el('table'),head=el('thead'),body=el('tbody');
        const row=(source,tag)=>{const tr=el('tr');for(const cell of source.replace(/^\s*\||\|\s*$/g,'').split('|')){const td=el(tag);td.append(inline(cell.trim(),options));tr.append(td);}return tr;};
        head.append(row(line,'th'));i+=2;while(i<lines.length&&lines[i].includes('|'))body.append(row(lines[i++],'td'));table.append(head,body);box.append(table);continue;
      }
      if(/^\s*>/.test(line)){const content=[];while(i<lines.length&&/^\s*>/.test(lines[i]))content.push(lines[i++].replace(/^\s*>\s?/,''));const quote=el('blockquote');quote.append(markdown(content.join('\n'),options));box.append(quote);continue;}
      const list=line.match(/^\s*(?:([-+*])|(\d+)[.)])\s+(.+)$/);
      if(list){const ul=el(list[2]?'ol':'ul');while(i<lines.length){const item=lines[i].match(/^\s*(?:[-+*]|\d+[.)])\s+(.+)$/);if(!item)break;const li=el('li');const task=item[1].match(/^\[([ xX])\]\s+(.*)$/);if(task){const check=el('input');check.type='checkbox';check.checked=task[1].toLowerCase()==='x';check.disabled=true;check.setAttribute('aria-label',check.checked?'Completed':'Open finding');li.className='markdown-checklist-item';li.append(check,inline(task[2],options));}else li.append(inline(item[1],options));ul.append(li);i++;}box.append(ul);continue;}
      const paragraph=[line];i++;while(i<lines.length&&lines[i].trim()&&!/^\s*(?:#{1,6}\s|`{3,}|~{3,}|>|[-+*]\s|\d+[.)]\s)/.test(lines[i]))paragraph.push(lines[i++]);append('p',paragraph.join('\n'));
    }
    return box;
  }
  // Concatenated file reads have no reliable file names. Preserve their text,
  // but split recognizable Markdown and source boundaries into readable blocks.
  function mixed(text){
    const blocks=[];let lines=[],kind='',fence=false;
    const flush=()=>{const value=lines.join('\n').trim();if(value)blocks.push({text:value,kind:kind||'text'});lines=[];};
    for(const line of text.split('\n')){
      if(/^\s*(`{3,}|~{3,})/.test(line)){if(!fence&&kind!=='markdown'){flush();kind='markdown';}fence=!fence;lines.push(line);continue;}
      if(!fence){let next='';
        if(/^#{1,6}\s+\S/.test(line))next='markdown';
        else if(/^package\s+\w+/.test(line))next='go';
        else if(/^#!.*(?:bash|sh)/.test(line))next='bash';
        else if(/^diff --git /.test(line))next='diff';
        else if(/^\s*\{\s*$/.test(line)&&!kind)next='json';
        if(next&&next!==kind){flush();kind=next;}
      }
      lines.push(line);
    }
    flush();return blocks;
  }
  window.MitosFormat={code,highlight,markdown,mixed,language,inline};
})();
