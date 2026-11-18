const {test} = require('node:test');
const assert = require('node:assert/strict');
global.window = {};
require('../static/execution-policy.js');
const {initial,codingAgentOptions} = window.MitosExecutionPolicy;

const codex = {id:'codex',kind:'agent',capabilities:{coordinated_work:true},installed:true,harness_environments:[{profile_id:'',models:[
 {id:'big',default:true,efforts:['high']}, {id:'small',efforts:['low','medium']}
]}]};
const claude = {id:'claude',kind:'agent',capabilities:{coordinated_work:true},installed:false,harness_environments:[{profile_id:'work',models:[
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
 const missing={...codex,harness_environments:[],profiles:[{id:'broken',executable:'/missing'}]};
 const [h]=initial({},[missing,{...claude,harness_environments:[{profile_id:'work'}]}]).harnesses;
 assert.equal(h.runtime,'claude');
 assert.deepEqual(h.tiers.high,{model:'',effort:'high'});
 const fixed=initial({},[{...codex,harness_environments:[{profile_id:'',models:[{id:'fixed',efforts:[]}]}]}]);
 assert.deepEqual(fixed.harnesses[0].tiers.high,{model:'fixed',effort:''});
});

test('installed agents without a coordinated adapter stay out of worker and orchestrator assignments',()=>{
 const pi={id:'pi',name:'Pi',kind:'agent',installed:true,capabilities:{coordinated_work:false},harness_environments:[{profile_id:''}]};
 const inventory=[pi,claude,{...pi,id:'absent',installed:false,harness_environments:[]},{...pi,id:'utility',kind:'application'}];
 const choices=codingAgentOptions(inventory,claude.id);
 assert.deepEqual(choices.map(([id,,disabled])=>[id,disabled]),[['pi',true],['claude',false]]);
 assert.match(choices[0][1],/installed/);
 const policy=initial({},inventory);
 assert.deepEqual(policy.harnesses.map(h=>h.runtime),['claude']);
 assert.deepEqual(policy.allowed_harnesses,['default']);
});
