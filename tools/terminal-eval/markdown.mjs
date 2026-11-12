// Browser contract checks for the shared light-DOM Markdown surface, without a server/model.
import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH,args:['--no-sandbox']});
try{
 const page=await browser.newPage({viewport:{width:1440,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.setContent('<html><head></head><body><form id="test"><label for="instructions">Instructions</label><textarea id="instructions" name="instructions" data-markdown="Instructions.md" required># Hello\n\nExisting **instructions**.</textarea><button type="submit">Save</button></form><article id="viewer"></article></body></html>');
 for(const name of ['operating.css','work-graph.css','markdown-editor.css'])await page.addStyleTag({path:path.join(root,'internal/web/static',name)});
 for(const name of ['analysis-format.js','markdown-editor.js'])await page.addScriptTag({path:path.join(root,'internal/web/static',name)});
 const editor=page.locator('#test mitos-markdown'),input=page.locator('#instructions');
 assert.equal(await editor.evaluate(e=>e.shadowRoot),null);assert.equal(await input.getAttribute('name'),'instructions');assert.equal(await editor.locator('.md-gutter').textContent(),'1\n2\n3');
 await input.fill('Keep this selection');await input.evaluate(e=>e.setSelectionRange(5,9));await editor.getByRole('button',{name:'Bold',exact:true}).click();assert.equal(await input.inputValue(),'Keep **this** selection');await input.press('Control+z');assert.equal(await input.inputValue(),'Keep this selection');await input.press('Control+Shift+z');assert.equal(await input.inputValue(),'Keep **this** selection');
 const selection=await input.evaluate(e=>[e.selectionStart,e.selectionEnd]);await editor.getByRole('button',{name:'Preview',exact:true}).click();await editor.locator('.md-preview strong').waitFor();assert.equal(await editor.locator('.md-preview strong').textContent(),'this');await editor.getByRole('button',{name:'Split',exact:true}).click();assert.equal(await input.isVisible(),true);assert.deepEqual(await input.evaluate(e=>[e.selectionStart,e.selectionEnd]),selection);
 assert.equal(await page.evaluate(()=>new FormData(document.getElementById('test')).get('instructions')),'Keep **this** selection');
 await editor.evaluate(e=>e.addEventListener('markdown-save',event=>{event.preventDefault();window.savedMarkdown=event.detail.value}));await input.press('Control+s');assert.equal(await page.evaluate(()=>window.savedMarkdown),'Keep **this** selection');
 await input.fill('First\nSecond\nThird');await input.evaluate(e=>{e.setSelectionRange(0,e.value.length)});await editor.getByRole('button',{name:'Checklist',exact:true}).click();assert.equal(await input.inputValue(),'- [ ] First\n- [ ] Second\n- [ ] Third');assert.equal(await editor.locator('.markdown-checklist-item').count(),3);
 await input.fill(Array.from({length:100},(_,i)=>'line '+i).join('\n'));await input.evaluate(e=>{e.scrollTop=500;e.dispatchEvent(new Event('scroll'))});assert.ok((await editor.locator('.md-gutter pre').getAttribute('style')).includes('-500px'));
 await page.evaluate(()=>document.getElementById('test').reset());await page.waitForTimeout(30);assert.equal(await input.inputValue(),'# Hello\n\nExisting **instructions**.');assert.ok(!(await editor.locator('.md-status').textContent()).includes('Unsaved'));
 await page.evaluate(()=>{const host=window.MitosMarkdown.view('# Result\n\n[Evidence](saved.md)\n\n<img src=x onerror="window.pwned=1">\n\n[bad](javascript:alert(1))\n\n```js\nconst answer = 42;\n```',{filename:'Report.md',link:(url,label)=>{if(url!=='saved.md')return null;const b=document.createElement('button');b.textContent=label;b.onclick=()=>window.openedEvidence=true;return b}});document.getElementById('viewer').append(host)});
 const viewer=page.locator('#viewer mitos-markdown');assert.equal(await viewer.locator('img,script,a[href^="javascript:"]').count(),0);await viewer.getByRole('button',{name:'Evidence',exact:true}).click();assert.equal(await page.evaluate(()=>window.openedEvidence),true);await viewer.getByRole('button',{name:'Source',exact:true}).click();assert.equal(await viewer.locator('textarea').getAttribute('readonly'),'');await viewer.getByRole('button',{name:'Preview',exact:true}).click();await viewer.getByRole('button',{name:'Evidence',exact:true}).click();assert.ok(await viewer.locator('.syntax-keyword').count());
 // Enhancement is idempotent, including dynamically added forms and reattachment.
 await page.evaluate(()=>{window.MitosMarkdown.enhance();const form=document.createElement('form');form.id='dynamic';form.innerHTML='<textarea name="body" data-markdown="Notes.md">A note</textarea>';document.body.append(form)});await page.locator('#dynamic mitos-markdown').waitFor();assert.equal(await page.locator('#test mitos-markdown mitos-markdown').count(),0);
 await page.setViewportSize({width:390,height:844});await page.waitForTimeout(40);assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));assert.equal(await editor.evaluate(e=>e.classList.contains('md-narrow')),true);
 await page.screenshot({path:'/tmp/mitos-markdown-mobile.png',fullPage:true});await page.setViewportSize({width:1440,height:1000});await page.screenshot({path:'/tmp/mitos-markdown-editor.png',fullPage:true});assert.deepEqual(errors,[]);
 console.log('PASS: Markdown modes, selection, formatting, native undo, forms/reset, save shortcut, linked evidence, safe preview, dynamic reuse and mobile layout');
}finally{await browser.close()}
