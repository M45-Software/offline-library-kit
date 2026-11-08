'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const html=fs.readFileSync(path.join(__dirname,'../../index.html'),'utf8');
function navigate(state,previous,step){const context=vm.createContext({state,previous,step});vm.runInContext(html.slice(html.indexOf('function hasReadableLibrary('),html.indexOf('function renderLibraryNavigation(')),context);return JSON.parse(vm.runInContext('JSON.stringify(navigationForLibrary(state,previous,step))',context))}
const empty={root:'one',archiveCount:0,queue:[{id:'pending'}],partials:[{name:'partial.zim'}],job:{state:'idle'}};
const ready={...empty,archiveCount:1};
test('queued and partial files do not expose reading; installed content becomes the main view',()=>{
 assert.equal(navigate(empty,null,'read').ready,false);assert.equal(navigate(empty,null,'read').landing,'location');
 assert.equal(navigate(ready,null,'location').landing,'read');
 assert.equal(navigate({...ready,libraryError:'missing'},null,'read').landing,'location');
});
test('management stays selected across progress polls; changing libraries resets the landing',()=>{
 const previous=navigate(ready,null,'location');assert.equal(navigate(ready,previous,'manage').landing,'manage');
 assert.equal(navigate({...ready,root:'two'},previous,'collections').landing,'read');
 assert.equal(navigate(empty,previous,'read').landing,'location');
});
test('first download exposes reading while retaining progress until completion',()=>{
 const previous=navigate(empty,null,'download');
 const active=navigate({...ready,queueActive:true,job:{state:'indexing'}},previous,'download');
 assert.equal(active.ready,true);assert.equal(active.landing,'download');assert.equal(active.pending,true);
 assert.equal(navigate({...ready,job:{state:'complete'}},active,'download').landing,'read');
 assert.equal(navigate({...ready,job:{state:'failed'}},active,'download').landing,'download');
 assert.equal(navigate({...ready,job:{state:'canceled'}},active,'download').landing,'download');
});
