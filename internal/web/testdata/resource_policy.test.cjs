const {test}=require('node:test');
const assert=require('node:assert/strict');
require('../static/resource-policy.js');
const {policy,signature}=globalThis.MitosResourcePolicy;
test('legacy preferences migrate without changing budgets',()=>{
 const old={minutes:45,attempts:12};
 const modern={resources:{workflow:{elapsed_minutes:{mode:'limit',amount:45},attempts:{mode:'limit',amount:12}}}};
 assert.equal(signature(old),signature(modern));
 assert.deepEqual(policy(old).workflow.tokens,{mode:'inherit'});
});
test('changes to every scoped dimension participate in dirty/proposal checks',()=>{
 const original=signature({});
 for(const [scope,key] of [['workflow','elapsed_minutes'],['workflow','attempts'],['workflow','tokens'],['agent','cpu_millicores'],['agent','memory_mib']]){
  assert.notEqual(signature({resources:{[scope]:{[key]:{mode:'limit',amount:20}}}}),original);
 }
 assert.notEqual(signature({resources:{workflow:{tokens:{mode:'unlimited'}}}}),original);
 assert.equal(signature({resources:{agent:{memory_mib:{mode:'inherit'}}}}),original);
});
test('serialization preserves explicit zero so validation can reject it',()=>{
 assert.deepEqual(policy({resources:{workflow:{attempts:{mode:'limit',amount:0}}}}).workflow.attempts,{mode:'limit',amount:0});
});
