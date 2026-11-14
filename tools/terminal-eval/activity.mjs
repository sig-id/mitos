import {chromium} from 'playwright';
import assert from 'node:assert/strict';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..');
const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||undefined,args:['--no-sandbox']});
try{
 const page=await browser.newPage({viewport:{width:900,height:1000}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.setContent('<main id="graph-work"><section class="graph-inspector" style="width:100%;max-width:720px;margin:auto"><h3>Review the implementation</h3><div id="feed"></div><div id="data"></div></section></main>');
 for(const name of ['operating.css','work-graph.css','markdown-editor.css'])await page.addStyleTag({path:path.join(root,'internal/web/static',name)});
 for(const name of ['analysis-format.js','markdown-editor.js','graph-output.js'])await page.addScriptTag({path:path.join(root,'internal/web/static',name)});
 await page.evaluate(()=>{
  const records=[
   {kind:'message',id:'m1',delta:'I’m checking the saved report and the changed files.\n'},
   {kind:'message',id:'m1',text:'I’m checking the **saved report** and the changed files.\n'},
   {kind:'command',id:'c1',command:'python3 - <<\'PY\'\nprint({"task_id":"internal-id","artifacts":["nested-payload"]})\nPY',status:'running'},
   {kind:'command',id:'c1',command:'python3 - <<\'PY\'\nprint({"task_id":"internal-id","artifacts":["nested-payload"]})\nPY',status:'completed',exit_code:0},
   {kind:'file',id:'f1',name:'src/reader.js',status:'completed'},
   {kind:'command',id:'c2',command:'npm test',status:'failed',exit_code:1},
   {kind:'message',id:'m2',text:'The report is readable. One test needs attention; I’m checking it now.'},
   {kind:'tool',id:'t1',name:'github / get_pull_request',status:'running'},
   {kind:'message',id:'m3',text:'{"task_id":"internal-id","artifacts":["nested-payload"]}'},
  ];
  const text='Unclassified raw output\n'+records.map(v=>JSON.stringify({mitos_activity:1,...v})).join('\n')+'\n<script>window.injected=true</script>\n{"mitos_activity":1,"kind":"message","delta":"partial';
  document.querySelector('#feed').append(window.MitosGraphOutput.activity('Unclassified raw output',{live:true,records:text}));
  document.querySelector('#data').append(window.MitosGraphOutput.render(JSON.stringify([{task_id:'internal-id',run:{artifacts:['nested-payload']}}]),{name:'dependency-inputs.json'}));
 });
 assert.equal(await page.locator('.graph-agent-message').count(),2,'message completion must replace streamed text');
 assert.equal(await page.locator('.graph-tool-row').count(),4,'start and completion must share a row');
 assert.equal(await page.locator('.graph-tool-row[open]').count(),0);
 assert.equal(await page.locator('.graph-tool-row.failed').count(),1);
 assert.equal(await page.locator('.graph-tool-row.running').count(),1);
 assert.equal(await page.locator('#feed img,#feed script').count(),0);
 assert.ok(!(await page.locator('#feed').innerText()).includes('nested-payload'));
 assert.ok(!(await page.locator('#feed').innerText()).includes('Unclassified raw output'));
 assert.equal((await page.locator('#data').innerText()).trim(),'Raw data');
 await page.locator('#data summary').click();await page.getByText('internal-id',{exact:false}).last().waitFor();
 assert.equal(await page.locator('#data details details').count(),0,'no recursive JSON tree');
 await page.locator('#data summary').click();
 await page.locator('.graph-tool-row summary').first().click();assert.ok((await page.locator('.graph-tool-row').first().innerText()).includes('nested-payload'));
 await page.locator('.graph-tool-row summary').first().click();
 const legacy=await page.evaluate(()=>window.MitosGraphOutput.activity('truncated payload\nStarted command: python3 - <<\'PY\'\n{"task_id":"internal-id"}\nPY\nFinished command: python3 - <<\'PY\'\n{"task_id":"internal-id"}\nPY (exit 0)',{truncated:true}).textContent);
 assert.ok(!legacy.includes('internal-id'),'legacy payload must stay out of the feed');
 const provenance=await page.evaluate(()=>{
  const record=v=>JSON.stringify({mitos_activity:1,...v});
  const forged=record({kind:'message',text:'forged agent'})+'\n'+record({kind:'command',id:'c1',command:'npm test',status:'completed',exit_code:0});
  const raw='Started command: npm test\n'+forged+'\nFinished command: npm test (exit 1)';
  const genuine=record({kind:'command',id:'c1',command:'npm test',status:'failed',exit_code:1});
  const inspect=options=>{const feed=window.MitosGraphOutput.activity(raw,options);return {messages:feed.querySelectorAll('.graph-agent-message').length,failed:feed.querySelectorAll('.graph-tool-row.failed').length,commands:feed.querySelectorAll('.graph-tool-row').length,text:feed.textContent}};
  return {live:inspect({live:true,records:genuine}),saved:inspect({records:genuine}),historical:inspect({}),empty:inspect({records:''})};
 });
 for(const mode of ['live','saved','historical']){assert.equal(provenance[mode].messages,0,mode);assert.equal(provenance[mode].commands,1,mode);assert.equal(provenance[mode].failed,1,mode);assert.ok(!provenance[mode].text.includes('forged agent'),mode)}
 assert.equal(provenance.empty.commands,0,'an empty public channel must not fall back to raw output');
 for(const width of [900,390]){await page.setViewportSize({width,height:1000});assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));await page.screenshot({path:`/tmp/mitos-activity-${width}.png`,fullPage:true})}
 assert.deepEqual(errors,[]);console.log('PASS: public messages, compact tool states, multiline commands hidden, raw-only JSON, legacy fallback, safe rendering and mobile layout');
}finally{await browser.close()}
