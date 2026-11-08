'use strict';
const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const html=fs.readFileSync(path.join(__dirname,'../../index.html'),'utf8').replace('/*SEARCH_SPACE_PROFILES*/null',fs.readFileSync(path.join(__dirname,'../../search-space.json'),'utf8'));
function planner(){
 const context=vm.createContext({formatBytes:n=>(n/1e9).toFixed(2)+' GB'});
 // Exercise the control panel's actual planner without starting Electron.
 vm.runInContext(html.slice(html.indexOf('const curatedCollections ='),html.indexOf('// Icons are decorative')),context);
 vm.runInContext(html.slice(html.indexOf('const compactIDs='),html.indexOf('const groups=')),context);
 vm.runInContext(html.slice(html.indexOf('function runningQueueItems('),html.indexOf('function updatePresetSuggestion(')),context);
 return expression=>vm.runInContext(expression,context);
}
test('direct builds need peak space; staged destinations need finished space',()=>{
 const run=planner();
 run("const state={queue:[]};const selected=curatedCollections.filter(item=>presetIncludes(item,'256'));const plan=selectionFootprint(state,selected);const reserve=2*1073741824;");
 assert.equal(run('plan.total>plan.finalTotal'),true);
 assert.equal(run("recommendPreset({...state,freeBytes:plan.finalTotal+reserve},null,false,2,2).preset==='256'"),false);
 assert.equal(run("recommendPreset({...state,freeBytes:plan.total+reserve},{freeBytes:plan.finalTotal+reserve},true,2,2).preset"),'256');
 assert.notEqual(run("recommendPreset({...state,freeBytes:plan.total+reserve-1},{freeBytes:plan.finalTotal+reserve},true,2,2).preset"),'256');
 assert.notEqual(run("recommendPreset({...state,freeBytes:plan.total+reserve},{freeBytes:plan.finalTotal+reserve-1},true,2,2).preset"),'256');
});
test('suggest staging only when both drives fit, including reserves',()=>{
 const run=planner();
 run("const plan={total:12e9,finalTotal:8e9,unknown:false};const candidate={label:'SSD',freeBytes:12e9+32*1073741824,reserveGiB:32,physicalCheckAvailable:true};");
 assert.match(run('stagingRecommendation(plan,8e9,[candidate])'),/Stage on SSD/);
 assert.equal(run('stagingRecommendation(plan,8e9,[{...candidate,freeBytes:candidate.freeBytes-1}])'),'');
 assert.equal(run('stagingRecommendation(plan,8e9-1,[candidate])'),'');
 assert.equal(run('stagingRecommendation(plan,12e9,[candidate])'),'');
 assert.equal(run('stagingRecommendation({...plan,unknown:true},8e9,[candidate])'),'');
 assert.equal(run('stagingRecommendation(plan,8e9,[])'),'');
 assert.match(run('stagingRecommendation(plan,8e9,[{...candidate,physicalCheckAvailable:false}])'),/Confirm/);
});
test('a larger staged preset recommendation keeps the direct preset safe',()=>{
 const run=planner();
 run("const state={queue:[]};const plan=selectionFootprint(state,curatedCollections.filter(item=>presetIncludes(item,'512')));const result=recommendPreset({...state,freeBytes:plan.finalTotal+2*1073741824},null,false,2,2,[{label:'SSD',freeBytes:1e12,reserveGiB:32}]);");
 assert.equal(run('result.preset'),'256');
 assert.match(run('result.message'),/512 GB selection.*Stage on SSD/);
});
test('iFixit planning covers measured index peak separately from the finished index',()=>{
 const run=planner();
 run("const selected=[['iFixit repair guides','bff12bdb-1fac-6ab0-ed6c-9f5f030722b3',3570695757]];const plan=selectionFootprint({queue:[]},selected);");
 // Observed total with retained original, repaired copy and temporary indexing.
 assert.equal(run('plan.total>=3570695757+5768527872+682970668'),true);
 assert.equal(run('plan.finalTotal>=3625395353+682970668'),true);
 assert.equal(run('plan.total>plan.finalTotal+3570695757'),true);
});
test('finished indexes accumulate while temporary build work is reused',()=>{
 const run=planner();
 run("const a=['wikiHow','a',10e9],b=['wikiHow second edition','b',20e9];const pa=selectionFootprint({queue:[]},[a]),pb=selectionFootprint({queue:[]},[b]),both=selectionFootprint({queue:[]},[a,b]);const profile=collectionSearchSpace(a[1],a[0]);");
 assert.equal(run('both.finalTotal'),run('pa.finalTotal+pb.finalTotal-700000000'));
 assert.equal(run('both.total'),run('pa.total+pb.total-700000000-(profile.peakBytes-profile.finishedBytes)'));
});
test('larger archive sizes increase estimates without shrinking smaller editions',()=>{
 const run=planner();
 run('const measured={sourceBytes:100,peakBytes:1000,finishedBytes:100};');
 assert.equal(run('scaleSearchSpace(measured,50).peakBytes'),1000);
 assert.equal(run('scaleSearchSpace(measured,200).peakBytes'),2000);
 assert.equal(run('scaleSearchSpace(measured,200).finishedBytes'),200);
});

test('wikiHow allowances cover the measured peak and finished output',()=>{
 const run=planner();
 assert.ok(run("collectionSearchSpace('00000000-0000-0000-0000-000000000001','wikiHow',51256446526).peakBytes")>=3547209728);
 assert.ok(run("collectionSearchSpace('00000000-0000-0000-0000-000000000001','wikiHow',51256446526).finishedBytes")>=49931699);
});

test('all measured Gutenberg subjects have matching UI allowances with headroom',()=>{
 const run=planner();
 const measurements=JSON.parse(fs.readFileSync(path.join(__dirname,'../../../docs/gutenberg-space-measurements.json'),'utf8'));
 assert.equal(measurements.collections.length,12);
 for(const measured of measurements.collections){
  const profile=run(`collectionSearchSpace(${JSON.stringify(measured.archiveUUID)},'Gutenberg',${measured.sourceBytes})`);
  assert.ok(profile.peakBytes>=measured.peakAllocatedBytes*1.25,measured.name+' peak');
  assert.ok(profile.finishedBytes>=measured.finishedBytes*1.25,measured.name+' finished');
 }
});
