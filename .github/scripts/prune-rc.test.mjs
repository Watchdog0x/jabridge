import assert from 'node:assert/strict';
import test from 'node:test';
import {planPrune, rcVersion, validatePlan} from './prune-rc.mjs';

function release(n, extra={}) {
  const tag=`v1.0.0-rc.${n}`, name=`jabridge_1.0.0-rc.${n}_linux_amd64.tar.gz`;
  return {id:n,tag_name:tag,draft:false,prerelease:true,updated_at:'now',assets:[name,`${name}.sha256`,`${name}.sig`].map((name,id)=>({name,id,size:10,digest:`sha256:${'a'.repeat(64)}`})),...extra};
}
test('numeric RC ordering preserves stable releases and future drafts',()=>{
  const data=[release(9),release(22),release(10),release(23,{draft:true}),release(30,{tag_name:'v1.0.0',prerelease:false})];
  const plan=planPrune(data,'v1.0.0-rc.22');
  assert.equal(plan.keep.id,22);
  assert.deepEqual(plan.targets.map(r=>r.id),[9,10]);
});
test('old workflow cannot prune a newer published RC',()=>{
  assert.equal(planPrune([release(22),release(23)],'v1.0.0-rc.22').targets.length,0);
});
test('incomplete current release cannot trigger deletion',()=>{
  assert.throws(()=>planPrune([release(9),release(22,{assets:[]})],'v1.0.0-rc.22'));
});
test('unknown tag formats are outside scope',()=>{
  for(const tag of ['v1.0.0','v1.0.0-beta.1','../../oops','v1.0.0-rc.02']) assert.equal(rcVersion(tag),null);
});
test('apply detects replacement, missing releases and asset changes',()=>{
  const data=[release(9),release(22)], plan=planPrune(data,'v1.0.0-rc.22');
  validatePlan(plan,data,'v1.0.0-rc.22');
  assert.throws(()=>validatePlan(plan,[release(22)],'v1.0.0-rc.22'));
  assert.throws(()=>validatePlan(plan,[release(9,{updated_at:'later'}),release(22)],'v1.0.0-rc.22'));
  assert.throws(()=>validatePlan(plan,[release(9),release(22,{updated_at:'later'})],'v1.0.0-rc.22'));
  assert.throws(()=>validatePlan(plan,[...data,release(23)],'v1.0.0-rc.22'));
});
