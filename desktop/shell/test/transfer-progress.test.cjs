'use strict';
const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const html=fs.readFileSync(path.join(__dirname,'../../index.html'),'utf8');
function panel(){
 const context=vm.createContext({});
 vm.runInContext('const formatBytes='+html.match(/const formatBytes=([^;]+);/)[1]+';',context);
 vm.runInContext(html.slice(html.indexOf('function formatTransferETA('),html.indexOf('function updateQueueProgress(')),context);
 return expression=>vm.runInContext(expression,context);
}
test('download captions show rates and rounded transfer estimates',()=>{
 const run=panel();
 const caption=run("queueProgressText({state:'downloading',done:1e6,total:9e6,speedMeasured:true,bytesPerSecond:2e6,etaSeconds:4})");
 assert.match(caption,/1\.0 MB \/ 9\.0 MB/);
 assert.match(caption,/2\.0 MB\/s · about 4s transfer left/);
 for(const [seconds,expected] of [[59.1,'60s'],[60,'1m'],[3599,'1h'],[3660,'1h 1m'],[86401,'1d 1h']])assert.equal(run(`formatTransferETA(${seconds})`),expected);
});
test('warming, stalled, reconnecting and preparing jobs do not show stale ETAs',()=>{
 const run=panel();
 assert.equal(run("transferDetails({})"),'Measuring speed…');
 assert.match(run("transferDetails({speedMeasured:true,bytesPerSecond:0,etaSeconds:10})"),/ETA unavailable/);
 assert.match(run("transferDetails({speedMeasured:true,bytesPerSecond:1e6,done:100,total:100})"),/Transfer received/);
 assert.equal(run("queueProgressText({state:'retrying',error:'Reconnecting…',bytesPerSecond:1e6,etaSeconds:50})"),'Reconnecting…');
 assert.equal(run("queueProgressText({state:'indexing',bytesPerSecond:1e6,etaSeconds:50})"),'Preparing search…');
 assert.equal(run("formatTransferETA(Infinity)"),'');
 assert.equal(run("formatTransferETA(-1)"),'');
});
test('combined rate sums transfers and ETA uses the slowest current transfer',()=>{
 const run=panel();
 const summary=run("queueTransferSummary([{job:{state:'downloading',speedMeasured:true,bytesPerSecond:1e6,etaSeconds:2}},{job:{state:'downloading',speedMeasured:true,bytesPerSecond:3e6,etaSeconds:10}},{job:{state:'indexing',bytesPerSecond:100e6,etaSeconds:100}}],4)");
 assert.match(summary,/3 active · limit 4 · 2 downloading/);
 assert.match(summary,/4\.0 MB\/s combined/);
 assert.match(summary,/current transfers about 10s left/);
 assert.match(summary,/1 preparing/);
});
test('combined ETA stays unavailable until every transfer has a useful rate',()=>{
 const run=panel();
 assert.match(run("queueTransferSummary([{job:{state:'downloading',speedMeasured:true,bytesPerSecond:1e6,etaSeconds:2}},{job:{state:'downloading'}}],4)"),/Measuring speed…/);
 const stopped=run("queueTransferSummary([{job:{state:'downloading',speedMeasured:true,bytesPerSecond:0}},{job:{state:'downloading',speedMeasured:true,bytesPerSecond:1e6,etaSeconds:2}}],4)");
 assert.match(stopped,/1\.0 MB\/s combined · ETA unavailable/);
 assert.doesNotMatch(stopped,/current transfers about/);
});
