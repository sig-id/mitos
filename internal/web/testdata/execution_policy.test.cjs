const {test} = require('node:test');
const assert = require('node:assert/strict');
global.window = {};
require('../static/execution-policy.js');
const {initial} = window.MitosExecutionPolicy;

const codex = {id:'codex',installed:true,harness_environments:[{profile_id:'',models:[
 {id:'big',default:true,efforts:['high']}, {id:'small',efforts:['low','medium']}
]}]};
const claude = {id:'claude',installed:false,harness_environments:[{profile_id:'work',models:[
 {id:'opus'}, {id:'sonnet'}, {id:'haiku'}
]}]};

test('seeds compatible tier models from each runnable runtime and profile',()=>{
 const [a,b]=initial({},[codex,claude]).harnesses;
 assert.deepEqual([a.tiers.low.model,a.tiers.medium.model,a.tiers.high.model],['small','small','big']);
 assert.equal(b.profile_id,'work');
 assert.deepEqual([b.tiers.low.model,b.tiers.medium.model,b.tiers.high.model],['haiku','sonnet','opus']);
});
test('preserves project overrides and saved choices including the agent default',()=>{
 const policy=initial({model:'private-model'},[codex]);
 assert.equal(policy.harnesses[0].tiers.low.model,'private-model');
 policy.harnesses[0].tiers.low.model='';
 const restored=initial({execution:policy},[claude]);
 assert.deepEqual(restored,policy);
 assert.notEqual(restored,policy);
});
test('skips unresolved profiles and leaves an unavailable catalog editable',()=>{
 const missing={id:'codex',profiles:[{id:'broken',executable:'/missing'}]};
 const [h]=initial({},[missing,{...claude,harness_environments:[{profile_id:'work'}]}]).harnesses;
 assert.equal(h.runtime,'claude');
 assert.deepEqual(h.tiers.high,{model:'',effort:'high'});
 const fixed=initial({},[{...codex,harness_environments:[{profile_id:'',models:[{id:'fixed',efforts:[]}]}]}]);
 assert.deepEqual(fixed.harnesses[0].tiers.high,{model:'fixed',effort:''});
});
