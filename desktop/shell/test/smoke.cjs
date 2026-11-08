'use strict';
// Run against an assembled app folder and a verified small ZIM supplied by CI.
const { _electron } = require('playwright-core');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

(async () => {
  const executable = process.env.OFFLINE_LIBRARY_SMOKE_APP;
  const fixture = process.env.OFFLINE_LIBRARY_SMOKE_ZIM;
  const collisionFixture = process.env.OFFLINE_LIBRARY_SMOKE_COLLISION_ZIM;
  if (!executable || !fixture) throw new Error('Set OFFLINE_LIBRARY_SMOKE_APP and OFFLINE_LIBRARY_SMOKE_ZIM');
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'offline-library-smoke-'));
  // Brackets are legal folder characters on all supported platforms and must
  // not be treated as wildcard syntax when locating downloaded archives.
  const library = path.join(temp, 'library [fixture]');
  fs.mkdirSync(path.join(library, 'content', 'zim'), { recursive: true });
  fs.copyFileSync(fixture, path.join(library, 'content', 'zim', 'fixture.ZIM'));
  const partial = path.join(library, 'content', 'zim', 'unfinished.zim.part');
  fs.writeFileSync(partial, 'partial');
  fs.writeFileSync(partial + '.json', JSON.stringify({ name: 'unfinished.zim', bytes: 100,
    sha256: 'a'.repeat(64), url_sha256: 'b'.repeat(64), catalog_id: '600c47c6-a792-0f78-8fcc-9c05cf1c556b' }));
  fs.copyFileSync(path.resolve(__dirname, '../../../examples/index.html'), path.join(library, 'index.html'));
  fs.mkdirSync(path.join(library, 'content', 'manuals'), { recursive: true });
  fs.writeFileSync(path.join(library, 'content', 'manuals', 'sample.pdf'), '%PDF-1.4\nfixture');
  const env = { ...process.env, OFFLINE_LIBRARY_USER_DATA: path.join(temp, 'user-data'),
    XDG_CONFIG_HOME: path.join(temp, 'config'), XDG_CACHE_HOME: path.join(temp, 'cache'),
    OFFLINE_LIBRARY_ROOT: library, HTTP_PROXY: 'http://127.0.0.1:9', HTTPS_PROXY: 'http://127.0.0.1:9',
    ALL_PROXY: 'http://127.0.0.1:9', NO_PROXY: '127.0.0.1,localhost' };
  delete env.OFFLINE_LIBRARY_CORE;
  delete env.OFFLINE_LIBRARY_READER;
  let app;
  try {
    const noSandbox = process.platform === 'linux' && process.env.OFFLINE_LIBRARY_SMOKE_NO_SANDBOX === '1';
    const args = noSandbox ? ['--no-sandbox'] : [];
    // Playwright otherwise adds --no-sandbox to Electron on Linux by default.
    const chromiumSandbox = process.platform === 'linux' && !noSandbox;
    app = await _electron.launch({ executablePath: executable, args, env, chromiumSandbox, timeout: 60000 });
    let window = await app.firstWindow();
    if (process.platform === 'linux') {
      const sandbox = await app.evaluate(({ app, BrowserWindow }) => ({
        disabled: app.commandLine.hasSwitch('no-sandbox'),
        renderer: BrowserWindow.getAllWindows()[0].webContents.getLastWebPreferences().sandbox,
      }));
      assert.equal(sandbox.disabled, noSandbox);
      assert.equal(sandbox.renderer, true);
      const rendererPID=await app.evaluate(({BrowserWindow})=>BrowserWindow.getAllWindows()[0].webContents.getOSProcessId());
      const rendererStatus=fs.readFileSync('/proc/'+rendererPID+'/status','utf8');
      if(!noSandbox){assert.match(rendererStatus,/^Seccomp:\s+2$/m);assert.match(rendererStatus,/^NoNewPrivs:\s+1$/m)}
    }
    await window.getByText('1 collection available').waitFor({ timeout: 30000 });
    assert.equal(await window.getByRole('heading', { name: 'What would you like to explore?' }).isVisible(), true);
    await window.locator('#reading-browse article').first().waitFor({timeout:30000});
    if(process.env.OFFLINE_LIBRARY_SMOKE_SCREENSHOT)await window.screenshot({path:process.env.OFFLINE_LIBRARY_SMOKE_SCREENSHOT,fullPage:true});
    assert.equal(await window.locator('#management-steps').isVisible(),false);
    await window.locator('#manage-library').click();
    await window.getByRole('heading',{name:'Manage your library',exact:true}).waitFor();
    assert.equal(await window.locator('#library-home').isVisible(),true);
    await window.getByRole('button',{name:'1 Location',exact:true}).click();
    assert.equal(await window.getByRole('heading', { name: 'Choose your collections' }).isVisible(), false);
    await window.getByText('Advanced settings', { exact: true }).click();
    await window.evaluate(() => chooseDestinationMode('local', false));
    assert.equal(await window.locator('#reserve-gib').inputValue(), '32');
    // Exercise the location controls without switching this fixture library.
    await window.evaluate(() => chooseDestinationMode('external', false));
    assert.equal(await window.getByRole('radio', {name:/Download to a fast drive first/}).isChecked(),true);
    assert.equal(await window.locator('#reserve-gib').inputValue(), '32');
    await window.getByRole('radio', {name:/Download directly to the drive/}).check();
    assert.equal(await window.locator('#reserve-gib').inputValue(), '2');
    await window.evaluate(() => { selectedStrategy='staged'; renderStageOptions(); suggestStorageReserve(); });
    assert.equal(await window.locator('#reserve-gib').inputValue(), '32');
    await window.getByRole('radio', { name: /Download directly to the drive/ }).check();
    await window.evaluate(() => { selectedLibraryDrive='/fixture'; });
    let releaseBenchmark, benchmarkStarted, benchmarkCanceled = false;
    const benchmarkGate = new Promise(resolve => { releaseBenchmark = resolve; });
    const benchmarkRunning = new Promise(resolve => { benchmarkStarted = resolve; });
    await window.route('**/api/benchmark', async route => { benchmarkStarted(); await benchmarkGate; await route.fulfill({ status: 409, body: 'Canceled' }); });
    await window.route('**/api/download/cancel', async route => { benchmarkCanceled = true; releaseBenchmark(); await route.fulfill({ json: { canceling: true } }); });
    await window.getByRole('button', { name: 'Test this library drive' }).click();
    await benchmarkRunning;
    await window.getByRole('button', { name: 'Continue to collections' }).click();
    assert.equal(benchmarkCanceled, true);
    await window.unroute('**/api/benchmark');
    await window.unroute('**/api/download/cancel');
    await window.getByRole('heading', { name: 'Choose your collections' }).waitFor();
    // Existing integration checks exercise controls across all steps.
    await window.evaluate(() => { document.querySelectorAll('[data-panel]').forEach(p => { p.hidden = false; }); document.querySelector('#add-selected').hidden = false; });
    await window.getByText(/GB free for collections/).waitFor();
    assert.equal(await window.locator('#reserve-gib').inputValue(), '2');
    await window.locator('#reserve-gib').fill('64');
    await window.getByRole('button', { name: 'Save reserve' }).click();
    await window.getByText('Reserve saved.').waitFor();
    await window.getByText(/Available after reserve and active commitments:/).waitFor();
    const capacity = await window.evaluate(() => { renderQueue([], null, false, 100e9, 20e9); return document.querySelector('#queue-capacity').textContent; });
    assert.equal(capacity, 'Available after reserve and active commitments: 80.00 GB');
    await window.getByRole('heading', { name: 'Incomplete downloads', exact: true }).waitFor();
    await window.getByText(/unfinished\.zim/).waitFor();
    await window.getByRole('button', { name: 'Resume', exact: true }).waitFor();
    // Link repair is part of the normal download flow, with no separate action.
    await window.route('**/api/state', async route => route.fulfill({ json: {
      root: library, archiveCount: 1, freeBytes: 100e9, reserveBytes: 2*1073741824,
      queue: [], queueCurrent: {id:'bff12bdb-1fac-6ab0-ed6c-9f5f030722b3',title:'iFixit',approxBytes:4e9}, queueActive:true,
      job:{state:'repairing',name:'ifixit.zim',done:500,total:1000},partials:[]
    } }));
    await window.evaluate(() => update());
    assert.equal(await window.locator('#download-status').textContent(), 'Repairing links…');
    assert.equal(await window.locator('#progress').isVisible(), true);
    assert.equal(await window.locator('#progress').getAttribute('value'), '50');
    assert.equal(await window.getByRole('button', {name:'Pause download list',exact:true}).isEnabled(),true);
    assert.equal(await window.getByRole('button', {name:'Start download list',exact:true}).isVisible(),false);
    const repairCapacity = await window.evaluate(() => {
      const old = document.querySelector('#reserve-gib').value;
      document.querySelector('#reserve-gib').value='2';
      const result=checkSelectionCapacity({freeBytes:10e9,queue:[],partials:[],archiveCount:0},null,[['iFixit','bff12bdb-1fac-6ab0-ed6c-9f5f030722b3',4e9]]);
      document.querySelector('#reserve-gib').value=old;
      return result;
    });
    assert.equal(repairCapacity.blocked,true); // Original fits; original plus repaired copy does not.
    await window.unroute('**/api/state');
    await window.evaluate(() => update());
    // A late response for an older drive state must not re-enable actions.
    let releaseState, signalState;
    const stateGate = new Promise(resolve => { releaseState = resolve; });
    const stateStarted = new Promise(resolve => { signalState = resolve; });
    let stateRequests = 0;
    await window.route('**/api/state', async route => {
      const request = ++stateRequests;
      if (request === 1) { signalState(); await stateGate; }
      await route.fulfill({ json: { root: library, archiveCount: 1, job: { state: 'idle' },
        ...(request === 1 ? {} : { libraryError: 'Library folder is missing or has changed' }) } });
    });
    await window.evaluate(() => { globalThis.stateTestFirst = update(); });
    await stateStarted;
    await window.evaluate(() => update());
    await window.getByText(/Library folder is missing or has changed/).waitFor();
    releaseState();
    await window.evaluate(() => globalThis.stateTestFirst);
    assert.equal(await window.locator('#start').isDisabled(), true);
    assert.match(await window.locator('#summary').textContent(), /Library folder is missing or has changed/);
    await window.unroute('**/api/state');
    await window.getByText('1 collection available').waitFor({ timeout: 10000 });
    // Simulate a mountpoint remaining after its drive is removed. No download
    // directories should appear in the replacement folder.
    const displaced = library + '-displaced';
    for (let attempt = 0; ; attempt++) {
      try { fs.renameSync(library, displaced); break; }
      catch (error) { if (attempt >= 20) throw error; await new Promise(r => setTimeout(r, 50)); }
    }
    fs.mkdirSync(library);
    await window.getByText(/Library folder is missing or has changed/).waitFor({ timeout: 10000 });
    assert.equal(await window.locator('#add').isDisabled(), true);
    assert.equal(await window.locator('#start').isDisabled(), true);
    assert.equal(fs.existsSync(path.join(library, 'content')), false);
    fs.rmdirSync(library);
    fs.renameSync(displaced, library);
    await window.getByText('1 collection available').waitFor({ timeout: 10000 });
    // An invalid collection directory must not look like an empty library.
    const zimDirectory = path.join(library, 'content', 'zim');
    const savedDirectory = zimDirectory + '-saved';
    fs.renameSync(zimDirectory, savedDirectory);
    fs.writeFileSync(zimDirectory, 'not a directory');
    await window.getByText(/Cannot read collection folder/).waitFor({ timeout: 10000 });
    assert.equal(await window.locator('#start').isDisabled(), true);
    fs.unlinkSync(zimDirectory);
    fs.renameSync(savedDirectory, zimDirectory);
    await window.getByText('1 collection available').waitFor({ timeout: 10000 });
    await window.locator('#manage-library').click();
    await window.evaluate(()=>document.querySelectorAll('[data-panel]').forEach(panel=>panel.hidden=false));
    await window.getByRole('button', { name: 'Resume', exact: true }).waitFor();
    await window.getByRole('button', { name: 'Discard incomplete download unfinished.zim' }).click();
    await window.getByText('Incomplete download discarded.').waitFor();
    assert.equal(fs.existsSync(partial), false);
    assert.equal(fs.existsSync(partial + '.json'), false);
    assert.equal(fs.existsSync(path.join(library, 'content', 'zim', 'fixture.ZIM')), true);
    const recommendations = await window.evaluate(() => {
      const state={queue:[],partials:[],archiveCount:0};
      const plans=Object.fromEntries(['compact','256','512'].map(preset=>[preset,selectionFootprint(state,curatedCollections.filter(item=>presetIncludes(item,preset)))]));
      const choose=(bytes,destination=null,staged=false,reserve=2)=>recommendPreset({...state,freeBytes:bytes},destination,staged,reserve,2).preset;
      const reserve=2*1073741824;
      return {
        exact:['compact','256','512'].map(preset=>choose(plans[preset].total+reserve)),
        below:choose(plans.compact.total+reserve-1),
        destinationLimited:choose(1e12,{freeBytes:plans.compact.finalTotal+reserve},true),
        stagingLimited:choose(plans['256'].total+reserve,{freeBytes:1e12},true),
        reserveLimited:choose(plans['512'].total+reserve,null,false,32),
        unknown:recommendPreset({...state,freeBytes:1e12,queue:[{id:'unknown',title:'Unknown',approxBytes:0}]},null,false,2,2).preset,
        unavailable:recommendPreset(state,null,false,2,2).preset,
        sameDrive:choose(1e12,{freeBytes:1e12,sameFilesystem:true},true)
      };
    });
    assert.deepEqual(recommendations,{exact:['compact','256','512'],below:null,destinationLimited:'compact',stagingLimited:'256',reserveLimited:'256',unknown:null,unavailable:null,sameDrive:null});
    const automaticChoices=await window.evaluate(()=>{
      const old={state:lastLibraryState,mode:selectedDestinationMode,strategy:selectedStrategy,drive:selectedLibraryDrive,collectionMode,automaticPreset,step:activeSetupStep,ready:collectionPreferencesReady,downloading,reserve:$('#reserve-gib').value,ids:[...document.querySelectorAll('#curated-collections input:checked')].map(input=>input.value)};
      try{
        selectedDestinationMode='external';selectedStrategy='direct';selectedLibraryDrive='/fixture';activeSetupStep='collections';collectionPreferencesReady=true;downloading=false;collectionMode='auto';automaticPreset=undefined;$('#reserve-gib').value='2';
        const choose=freeBytes=>{lastLibraryState={freeBytes,queue:[],partials:[],archiveCount:0};updateCuratedEstimate();return automaticPreset};
        const medium=choose(250e9),small=choose(32e9),large=choose(512e9);
        downloading=true;choose(1);const whileDownloading=automaticPreset;downloading=false;
        const none=choose(1),empty=document.querySelectorAll('#curated-collections input:checked').length;
        applyCollectionPreset('compact');collectionMode='custom';choose(512e9);
        return {medium,small,large,whileDownloading,none,empty,manualCount:document.querySelectorAll('#curated-collections input:checked').length};
      }finally{
        lastLibraryState=old.state;selectedDestinationMode=old.mode;selectedStrategy=old.strategy;selectedLibraryDrive=old.drive;collectionMode=old.collectionMode;automaticPreset=old.automaticPreset;activeSetupStep=old.step;collectionPreferencesReady=old.ready;downloading=old.downloading;$('#reserve-gib').value=old.reserve;
        document.querySelectorAll('#curated-collections input').forEach(input=>input.checked=old.ids.includes(input.value));updateCuratedEstimate();
      }
    });
    assert.deepEqual(automaticChoices,{medium:'256',small:'compact',large:'512',whileDownloading:'512',none:null,empty:0,manualCount:5});

    // The curated list is visible first; catalog search remains available.
    // Choose explicitly: the automatic preset depends on host free space.
    await window.locator('#collection-preset').selectOption('256');
    assert.equal(await window.getByRole('checkbox', { name: /^Wikipedia — no pictures —/ }).isChecked(), true);
    const presetGroups = await window.locator('#curated-collections [data-preset-group]').evaluateAll(groups => groups.map(group => group.dataset.presetGroup));
    assert.deepEqual(presetGroups, ['compact', '256', '512', 'developer']);
    const groupSizes = await window.evaluate(() => Array.from(document.querySelectorAll('[data-preset-group]')).map(group => Array.from(group.querySelectorAll('[data-collection-id]')).map(row => curatedCollections.find(item => item[1] === row.dataset.collectionId)[2])));
    for (const sizes of groupSizes) assert.deepEqual(sizes, [...sizes].sort((a, b) => b - a));
    const visualization = await window.evaluate(() => {
      const items = [{id:'small',title:'Small',approxBytes:100}, {id:'large',title:'Large',approxBytes:300}, {id:'unknown',title:'Unknown',approxBytes:0}];
      renderQueue(items, null, false, 1e9, 0);
      return { order: Array.from(document.querySelectorAll('#queue-list [data-queue-id]')).map(row => row.dataset.queueId), shares: Array.from(document.querySelectorAll('#queue-list .item-share')).map(node => node.textContent), original: items.map(item => item.id), icons: document.querySelectorAll('#queue-list .item-icon svg').length };
    });
    assert.deepEqual(visualization.order, ['small', 'large', 'unknown']);
    assert.deepEqual(visualization.original, ['small', 'large', 'unknown']);
    assert.deepEqual(visualization.shares, ['25.0%', '75.0%', 'Size unknown']);
    assert.equal(visualization.icons, 3);
    const beforePack=await window.locator('#curated-collections input:checked').evaluateAll(nodes=>nodes.map(node=>node.value));
    await window.getByRole('button',{name:'Add Fix things to selection',exact:true}).click();
    const afterPack=await window.locator('#curated-collections input:checked').evaluateAll(nodes=>nodes.map(node=>node.value));
    assert.ok(beforePack.every(id=>afterPack.includes(id)));
    assert.ok(afterPack.includes('bff12bdb-1fac-6ab0-ed6c-9f5f030722b3'));
    assert.equal(await window.locator('#collection-preset').inputValue(),'custom');
    // Active rows reserve the same action column even without Remove.
    const queueWidths = await window.evaluate(() => {
      const running=[{id:'active-a',title:'Active A',approxBytes:100,job:{state:'downloading',done:25,total:100,speedMeasured:true,bytesPerSecond:2e6,etaSeconds:10}},{id:'active-b',title:'Active B',approxBytes:100,job:{state:'retrying',error:'Reconnecting…'}}];
      renderQueue([{id:'pending',title:'Pending',approxBytes:100}],null,true,1e12,0,running);
      const remove=document.querySelector('#queue-list [data-queue-id=pending] .remove-item');remove.focus();renderQueue([{id:'pending',title:'Pending',approxBytes:100}],null,true,1e12,0,running.map(item=>({...item,job:{...item.job,done:30,bytesPerSecond:3e6,etaSeconds:8}})));if(document.activeElement!==remove)throw Error('Progress refresh replaced the focused Remove button');
      return [...document.querySelectorAll('#queue-list .queue-row')].map(row=>({caption:row.querySelector('.queue-current')?.textContent,width:row.querySelector('.size-track').getBoundingClientRect().width,remove:!!row.querySelector('.remove-item'),progress:!!row.querySelector('progress')}));
    });
    assert.equal(queueWidths.length,3);
    assert.equal(queueWidths[0].width,queueWidths[2].width);
    assert.equal(queueWidths[1].width,queueWidths[2].width);
    assert.deepEqual(queueWidths.map(item=>item.remove),[false,false,true]);
    assert.deepEqual(queueWidths.map(item=>item.progress),[true,true,false]);
    assert.match(queueWidths[0].caption,/3\.0 MB\/s · about 8s transfer left/);
    const itemPauseRequests=[];
    await window.route('**/api/queue/pause-item',async route=>{itemPauseRequests.push(route.request().postDataJSON().id);await route.fulfill({json:{paused:true}})});
    await window.getByRole('button',{name:'Pause Active A',exact:true}).click();
    assert.deepEqual(itemPauseRequests,['active-a']);
    await window.unroute('**/api/queue/pause-item');

    await window.locator('#download-bandwidth').fill('0.5');
    await window.getByRole('button',{name:'Save bandwidth limit',exact:true}).click();
    await window.waitForFunction(()=>lastLibraryState?.bandwidthKiB===512);
    assert.equal(await window.locator('#download-bandwidth').inputValue(),'0.5');

    const transferState=await window.evaluate(async()=>({...await api('/api/state'),queue:[],queueCurrent:null,queueActive:true,freeBytes:10e9,reserveBytes:2147483648,queueSpace:{reservedDownloadBytes:1e8,reservedPreparationBytes:2e8,remainingDownloadBytes:1e9,remainingUnknown:true},downloadHistory:[{title:'History fixture',state:'failed',time:'2026-11-07T12:00:00Z',retries:3,httpStatus:503,detail:'Download returned HTTP 503'}],queueRunning:[{id:'transfer-a',title:'Transfer A',job:{state:'downloading',done:1e6,total:10e6,speedMeasured:true,bytesPerSecond:2e6,etaSeconds:5}},{id:'transfer-b',title:'Transfer B',job:{state:'downloading',done:2e6,total:20e6,speedMeasured:true,bytesPerSecond:3e6,etaSeconds:6}},{id:'preparing',title:'Preparing',job:{state:'indexing'}}],job:{state:'indexing',name:'Preparing'}}));
    await window.route('**/api/state',async route=>route.fulfill({json:transferState}));
    await window.evaluate(async()=>await update());
    assert.match(await window.locator('#download-status').textContent(),/5\.0 MB\/s combined · current transfers about 6s left · 1 preparing/);
    assert.equal(await window.locator('#download-bandwidth').isEnabled(),true);
    assert.equal(await window.locator('#download-concurrency').isDisabled(),true);
    assert.match(await window.locator('#queue-capacity').textContent(),/7\.55 GB/);
    assert.match(await window.locator('#queue-space').textContent(),/Committed downloads100\.0 MB/);
    assert.match(await window.locator('#queue-space').textContent(),/At least 1\.00 GB · some sizes unknown/);
    await window.locator('#download-history summary').click();
    assert.match(await window.locator('#download-history p').textContent(),/3 automatic retries · Last error: HTTP 503/);
    const historyStable=await window.evaluate(async()=>{const entry=document.querySelector('#download-history details');await update();return entry===document.querySelector('#download-history details')&&entry.open});
    assert.equal(historyStable,true);

    await window.unroute('**/api/state');
    await window.evaluate(async()=>await update());

    const failedState=await window.evaluate(async()=>({...await api('/api/state'),queue:[{id:'failed-a',title:'Failed A',approxBytes:100,paused:true,networkFailure:true}],queueCurrent:null,queueRunning:[],queueActive:false,job:{state:'idle'}}));
    await window.route('**/api/state',async route=>route.fulfill({json:failedState}));
    await window.evaluate(async()=>await update());
    await window.getByText('Connection failed after 3 retries. Saved progress retained.',{exact:true}).waitFor();
    assert.equal(await window.locator('#queue-start').isDisabled(),true);
    assert.equal(await window.getByRole('button',{name:'Remove Failed A from download list',exact:true}).isVisible(),true);
    const failedRetryRequests=[];
    await window.route('**/api/queue/resume-item',async route=>{failedRetryRequests.push(route.request().postDataJSON().id);await route.fulfill({json:{paused:false}})});
    await window.getByRole('button',{name:'Retry Failed A',exact:true}).click();
    assert.deepEqual(failedRetryRequests,['failed-a']);
    await window.unroute('**/api/queue/resume-item');
    await window.unroute('**/api/state');
    await window.evaluate(async()=>await update());

    await window.locator('#download-bandwidth').fill('0');
    await window.getByRole('button',{name:'Save bandwidth limit',exact:true}).click();
    await window.waitForFunction(()=>lastLibraryState?.bandwidthKiB===0);

    assert.equal(await window.locator('#download-stall-seconds').inputValue(),'60');
    await window.locator('#download-stall-seconds').fill('120');
    await window.locator('#download-concurrency').fill('2');
    await window.getByRole('button',{name:'Save download settings'}).click();
    await window.getByText('Download settings saved.').waitFor();
    assert.equal(await window.locator('#download-concurrency').inputValue(),'2');
    assert.equal(await window.locator('#download-stall-seconds').inputValue(),'120');
    await window.locator('#download-stall-seconds').fill('60');
    await window.locator('#download-concurrency').fill('4');
    await window.getByRole('button',{name:'Save download settings'}).click();
    await window.waitForFunction(()=>lastLibraryState?.downloadConcurrency===4);
    const activeOrder = await window.evaluate(() => {
      renderQueue([{id:'large',title:'Large',approxBytes:300},{id:'unknown',title:'Unknown',approxBytes:0}],{id:'small',title:'Small',approxBytes:100},true,1e9,0);
      return Array.from(document.querySelectorAll('#queue-list [data-queue-id]')).map(row => row.dataset.queueId);
    });
    assert.deepEqual(activeOrder, ['small', 'large', 'unknown']);
    assert.equal(await window.locator('[data-panel=download]').getByText(/Sorted by size for review/).count(),0);

    assert.equal(await window.locator('[data-preset-group=developer]').getByRole('checkbox', { name: /Arduino/ }).count(), 1);
    await window.locator('#collection-preset').selectOption('compact');
    assert.equal(await window.locator('#curated-collections input:checked').count(), 5);
    assert.equal(await window.getByRole('checkbox', { name: /Best of Wikipedia — no pictures/ }).isChecked(), true);
    assert.equal(await window.getByRole('checkbox', { name: /wikiHow/ }).isChecked(), false);
    assert.equal(await window.getByRole('checkbox', { name: /Arduino/ }).isChecked(), false);
    assert.match(await window.locator('#curated-estimate').textContent(), /21\.10 GB/);
    await window.locator('#collection-preset').selectOption('256');
    assert.equal(await window.getByRole('checkbox', { name: /^Wikipedia — no pictures —/ }).isChecked(), true);
    assert.equal(await window.getByRole('checkbox', { name: /Wikipedia — with pictures/ }).isChecked(), false);
    await window.locator('#collection-preset').selectOption('512');
    assert.equal(await window.getByRole('checkbox', { name: /Gutenberg — science/ }).isChecked(), true);
    await window.locator('#curated-collections input:checked').evaluateAll(boxes => boxes.forEach(box => { box.checked = false; }));
    await window.getByRole('checkbox', { name: /Gutenberg — science/ }).check();
    await window.getByRole('button', { name: 'Add selected to download list' }).click();
    await window.getByRole('button', { name: 'Remove Gutenberg — science from download list' }).waitFor();
    // Pause a queued collection without starting a publisher download.
    await window.evaluate(async()=>{const item=(lastLibraryState.queue||[]).find(entry=>entry.title==='Gutenberg — science');await api('/api/queue/pause-item',{id:item.id});await update()});
    await window.getByRole('button',{name:'Resume Gutenberg — science',exact:true}).waitFor();
    const itemResumeRequests=[];
    await window.route('**/api/queue/resume-item',async route=>{itemResumeRequests.push(route.request().postDataJSON().id);await route.fulfill({json:{paused:false}})});
    await window.getByRole('button',{name:'Resume Gutenberg — science',exact:true}).click();
    assert.equal(itemResumeRequests.length,1);
    await window.unroute('**/api/queue/resume-item');

    await window.getByRole('button', { name: 'Add selected to download list' }).click();
    await window.getByText('0 added to the download list. 1 already in the list.').waitFor();
    await window.getByRole('button', { name: 'Remove Gutenberg — science from download list' }).click();
    await window.getByText('Search for more collections', { exact: true }).click();
    // A slower earlier catalog request must not overwrite a later topic search.
    let releaseSlow, signalSlow, signalFinished;
    const slowGate = new Promise(resolve => { releaseSlow = resolve; });
    const slowStarted = new Promise(resolve => { signalSlow = resolve; });
    const slowFinished = new Promise(resolve => { signalFinished = resolve; });
    await window.route('**/api/catalog?*', async route => {
      const query = new URL(route.request().url()).searchParams;
      assert.equal(query.get('lang'), 'fra');
      if (query.get('q') === 'medical') { signalSlow(); await slowGate; }
      const unknown = query.get('q') === 'unknown';
      const title = unknown ? 'Unknown size result' : query.get('q') === 'medical' ? 'Old medical result' : 'New repair result';
      await route.fulfill({ json: { total: 1, items: [{ id: unknown ? '994c47c6-a792-0f78-8fcc-9c05cf1c556b' : '600c47c6-a792-0f78-8fcc-9c05cf1c556b', title,
        summary: 'Fixture', language: 'fra', updated: '2026-01-01', approxBytes: unknown ? 0 : 1000 }] } });
      if (query.get('q') === 'medical') signalFinished();
    });
    await window.locator('#search select').selectOption('fra');
    await window.getByRole('button', { name: 'Medical', exact: true }).click(); await slowStarted;
    await window.getByRole('button', { name: 'Repair', exact: true }).click();
    await window.getByText('New repair result').waitFor();
    releaseSlow(); await slowFinished; await window.waitForTimeout(100);
    assert.equal(await window.getByText('Old medical result').count(), 0);
    assert.equal(await window.getByText('New repair result').count(), 1);
    await window.getByRole('button', { name: 'Add New repair result to download list' }).click();
    await window.getByRole('button', { name: 'Remove New repair result from download list' }).waitFor();
    assert.match(await window.locator('#queue-estimate').textContent(), /Build space: about 700\.0 MB · finished library: about 700\.0 MB/);
    assert.equal(await window.getByRole('button', { name: 'Start download list' }).isEnabled(), true);
    await window.getByRole('button', { name: 'Add New repair result to download list' }).click();
    await window.getByText('Collection is already in the list').waitFor();
    await window.locator('#search input[name=q]').fill('unknown');
    await window.getByRole('button', { name: 'Find collections' }).click();
    await window.getByRole('button', { name: 'Add Unknown size result to download list' }).click();
    await window.getByRole('button', { name: 'Remove Unknown size result from download list' }).waitFor();
    assert.match(await window.locator('#queue-estimate').textContent(), /700\.0 MB \(1 size unknown\)/);
    // The saved list survives an app restart but does not start by itself.
    await app.close(); app = null;
    app = await _electron.launch({ executablePath: executable, args, env, chromiumSandbox, timeout: 60000 });
    window = await app.firstWindow();
    await window.waitForFunction(()=>lastLibraryState!==null);
    await window.evaluate(() => { document.querySelectorAll('[data-panel]').forEach(p => { p.hidden = false; }); });
    await window.getByRole('button', { name: 'Remove New repair result from download list' }).waitFor({ timeout: 30000 });
    assert.equal(await window.locator('#reserve-gib').inputValue(), '64');
    assert.match(await window.locator('#queue-estimate').textContent(), /700\.0 MB \(1 size unknown\)/);
    assert.equal(await window.getByRole('button', { name: 'Start download list' }).isEnabled(), true);
    assert.equal(await window.getByRole('button', { name: 'Pause download list' }).isHidden(), true);
    await window.getByRole('button', { name: 'Remove New repair result from download list' }).click();
    await window.getByRole('button', { name: 'Remove Unknown size result from download list' }).click();
    await window.getByText('No collections in the list.').waitFor();
    await window.unroute('**/api/catalog?*');
    // A first-time user can populate the collection selector without opening
    // a separate reader window or running a search first.
    await window.locator('#reading-filters').evaluate(node=>node.open=true);
    await window.getByRole('button', { name: 'Load collections' }).click();
    await window.getByText('1 collection available for search.').waitFor({ timeout: 45000 });
    assert.equal(await window.locator('#library-collection option').count(), 2);
    assert.equal(await window.locator('#library-mode').inputValue(),'hybrid');
    await window.route('**/api/library/hybrid?*',async route=>{assert.equal(new URL(route.request().url()).searchParams.get('field'),'deep');assert.equal(new URL(route.request().url()).searchParams.get('kind'),'other');assert.equal(new URL(route.request().url()).searchParams.get('topic'),'reference');await route.fulfill({json:{hybrid:true,total:2,estimated:true,collectionsSearched:1,collectionsAttempted:1,results:[{title:'Hybrid result',collection:'Fixture',url:'http://127.0.0.1:8766/content/fixture/test',snippet:'Safe <script> text solar panel',authors:['Example Author'],subjects:['Learning']}],skipped:[],unavailable:[],hasMore:false,perCollectionLimit:60}})});
    await window.locator('#library-kind').selectOption('other');
    await window.locator('#library-topic').selectOption('reference');
    await window.locator('#library-query').fill('solar panel');
    await window.getByRole('button',{name:'Search library',exact:true}).click();
    await window.locator('#library-results a').filter({hasText:'Hybrid result'}).waitFor();
    assert.equal(await window.locator('#library-status').textContent(),'Showing 1 of about 2 matches · 1 collection searched.');
    assert.equal(await window.locator('#library-results script').count(),0);
    assert.match(await window.locator('#library-results').textContent(),/Safe <script> text/);
    assert.equal(await window.locator('#library-results .result-kind').count(),1);
    assert.match(await window.locator('#library-results').textContent(),/From Fixture/);
    assert.ok(await window.locator('#library-results mark').count()>0);
    assert.match(await window.locator('#library-results').textContent(),/By Example Author/);
    await window.locator('#library-kind').selectOption('');await window.locator('#library-topic').selectOption('');
    await window.unroute('**/api/library/hybrid?*');

    // A single-collection full-text search must not open an ambiguous reader path.
    await window.evaluate(() => {
      globalThis.conflictProbe = { ensureReader: globalThis.ensureReader, open: globalThis.open, closed: false };
      const id = '600c47c6-a792-0f78-8fcc-9c05cf1c556b';
      document.querySelector('#library-collection').add(new Option('Conflicting collection', id));
      globalThis.ensureReader = async () => { localItems = [{ id, fullText: true, addressConflict: true }]; return 'http://127.0.0.1:8766/'; };
      globalThis.open = () => ({ close() { globalThis.conflictProbe.closed = true; }, set opener(value) {} });
    });
    await window.locator('#library-collection').selectOption('600c47c6-a792-0f78-8fcc-9c05cf1c556b');
    await window.locator('#library-mode').selectOption('fulltext');
    await window.locator('#library-query').fill('repair');
    await window.getByRole('button', { name: 'Search library' }).click();
    await window.getByText('This collection shares a reader address with another archive and cannot be searched safely.').waitFor();
    assert.equal(await window.evaluate(() => globalThis.conflictProbe.closed), true);
    await window.evaluate(async () => { globalThis.ensureReader = globalThis.conflictProbe.ensureReader; globalThis.open = globalThis.conflictProbe.open; await loadLocalCollections(); });
    // A superseded single-collection full-text search closes its blank tab
    // and cannot replace the status or navigate after a newer search starts.
    await window.evaluate(() => {
      const originalEnsure = globalThis.ensureReader, originalOpen = globalThis.open;
      globalThis.popupRace = { originalEnsure, originalOpen, started: false, closed: false, navigated: false };
      globalThis.open = () => ({ close() { globalThis.popupRace.closed = true; }, set location(value) { globalThis.popupRace.navigated = true; }, set opener(value) {} });
      globalThis.ensureReader = () => {
        if (!globalThis.popupRace.started) {
          globalThis.popupRace.started = true;
          return new Promise(resolve => { globalThis.popupRace.release = () => resolve('http://127.0.0.1:8766/'); });
        }
        return originalEnsure();
      };
    });
    await window.locator('#library-collection').selectOption({ index: 1 });
    await window.locator('#library-mode').selectOption('fulltext');
    await window.locator('#library-query').fill('old full text');
    await window.getByRole('button', { name: 'Search library' }).click();
    await window.waitForFunction(() => globalThis.popupRace.started);
    await window.locator('#library-mode').selectOption('titles');
    await window.locator('#library-query').fill('computed');
    await window.getByRole('button', { name: 'Search library' }).click();
    await window.locator('#library-results a').filter({ hasText: 'Computed Properties' }).first().waitFor({ timeout: 45000 });
    assert.match(await window.locator('#library-status').textContent(), /1 of 1 collection\(s\) searched\. Page 1 shows up to 15 results per collection/);
    await window.evaluate(() => globalThis.popupRace.release());
    await window.waitForTimeout(100);
    assert.deepEqual(await window.evaluate(() => ({ closed: globalThis.popupRace.closed, navigated: globalThis.popupRace.navigated })), { closed: true, navigated: false });
    await window.evaluate(() => { globalThis.ensureReader = globalThis.popupRace.originalEnsure; globalThis.open = globalThis.popupRace.originalOpen; });
    await window.locator('#library-collection').selectOption('');
    // Starting a newer search aborts the older request, not merely its display.
    await window.evaluate(() => {
      globalThis.searchAbortProbe = { started: false, aborted: false, fetch: globalThis.fetch };
      globalThis.fetch = (url, options) => {
        if (!String(url).startsWith('/api/library/search?')) return globalThis.searchAbortProbe.fetch.call(globalThis, url, options);
        const query = new URL(String(url), location.href).searchParams.get('q');
        if (query === 'slowtest') return new Promise((resolve, reject) => {
          globalThis.searchAbortProbe.started = true;
          options.signal.addEventListener('abort', () => {
            globalThis.searchAbortProbe.aborted = true;
            reject(new DOMException('Aborted', 'AbortError'));
          }, { once: true });
        });
        return Promise.resolve(new Response(JSON.stringify({ results: [{ title: 'New search result', collection: 'Fixture', url: 'http://127.0.0.1:8766/content/fixture/example' }], unavailable: [], skipped: [], collectionsSearched: 1, collectionsAttempted: 1, perCollectionLimit: 15, hasMore: false }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
      };
    });
    await window.locator('#library-query').fill('slowtest');
    await window.getByRole('button', { name: 'Search library' }).click();
    await window.waitForFunction(() => globalThis.searchAbortProbe.started);
    await window.locator('#library-query').fill('newtest');
    await window.getByRole('button', { name: 'Search library' }).click();
    await window.getByText('New search result').waitFor();
    assert.equal(await window.evaluate(() => globalThis.searchAbortProbe.aborted), true);
    await window.evaluate(() => { globalThis.fetch = globalThis.searchAbortProbe.fetch; });
    await window.locator('#library-query').fill('computed');
    await window.getByRole('button', { name: 'Search library' }).click();
    await window.locator('#library-results a').filter({ hasText: 'Computed Properties' }).first().waitFor({ timeout: 45000 });
    await window.locator('#library-query').fill('component');
    await window.getByRole('button', { name: 'Search library' }).click();
    await window.getByRole('button', { name: 'Next results' }).waitFor({ state: 'visible', timeout: 30000 });
    const firstResult = await window.locator('#library-results a').first().textContent();
    await window.getByRole('button', { name: 'Next results' }).click();
    await window.getByRole('button', { name: 'Previous results' }).waitFor({ state: 'visible', timeout: 30000 });
    assert.notEqual(await window.locator('#library-results a').first().textContent(), firstResult);
    await window.getByRole('button', { name: 'Previous results' }).click();
    await window.getByRole('button', { name: 'Next results' }).waitFor({ state: 'visible', timeout: 30000 });
    assert.equal(await window.locator('#library-results a').first().textContent(), firstResult);
    const nextWindow = app.waitForEvent('window');
    await window.getByText('Open the full library and reader controls',{exact:true}).click();
    await window.getByRole('button', { name: 'Open library' }).click();
    const reader = await nextWindow;
    await reader.waitForURL('http://127.0.0.1:8765/', { timeout: 30000 });
    await window.getByText('Reader is running.').waitFor({ timeout: 30000 });
    const manual = await reader.request.get('http://127.0.0.1:8765/content/manuals/sample.pdf');
    assert.equal(manual.status(), 200);
    await reader.locator('#search').click();
    await reader.waitForURL('http://127.0.0.1:8766/', { timeout: 30000 });
    await reader.getByText('Vue Docs').first().waitFor({ timeout: 30000 });
    assert.equal(await window.locator('#choose-library').count(), 1);
    // A newly added archive becomes searchable after a user-requested refresh.
    if (collisionFixture) {
      fs.copyFileSync(collisionFixture, path.join(library, 'content', 'zim', 'second.zim'));
      assert.ok(await window.locator('#library-results a').count() > 0);
      await window.getByRole('button', { name: 'Refresh collections' }).click();
      await window.getByText('Reader refreshed: 2 collections available.').waitFor({ timeout: 45000 });
      assert.equal(await window.locator('#library-results a').count(), 0);
      await window.getByText('Collections refreshed. Search again for current results.').waitFor();
      assert.equal(await window.locator('#library-collection option').count(), 3);
      await window.locator('#library-query').fill('computed');
      await window.getByRole('button', { name: 'Search library' }).click();
      await window.locator('#library-results a').filter({ hasText: 'Computed Properties' }).first().waitFor({ timeout: 45000 });
      // A slow response from the old reader must not reappear after refresh,
      // even if the underlying request ignores its abort signal.
      await window.evaluate(() => {
        globalThis.refreshRace = { fetch: globalThis.fetch, started: false, aborted: false };
        globalThis.fetch = (url, options) => {
          if (!String(url).startsWith('/api/library/search?')) return globalThis.refreshRace.fetch.call(globalThis, url, options);
          return new Promise(resolve => {
            globalThis.refreshRace.started = true;
            options.signal.addEventListener('abort', () => { globalThis.refreshRace.aborted = true; }, { once: true });
            globalThis.refreshRace.release = () => resolve(new Response(JSON.stringify({ results: [{ title: 'Stale reader result', collection: 'Fixture', url: 'http://127.0.0.1:8766/content/fixture/example' }], unavailable: [], skipped: [], collectionsSearched: 1, collectionsAttempted: 1, perCollectionLimit: 15, hasMore: false }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
          });
        };
      });
      await window.getByRole('button', { name: 'Search library' }).click();
      await window.waitForFunction(() => globalThis.refreshRace.started);
      await window.getByRole('button', { name: 'Refresh collections' }).click();
      await window.getByText('Reader refreshed: 2 collections available.').waitFor({ timeout: 45000 });
      assert.equal(await window.evaluate(() => globalThis.refreshRace.aborted), true);
      await window.evaluate(() => { globalThis.refreshRace.release(); globalThis.fetch = globalThis.refreshRace.fetch; });
      await window.waitForTimeout(100);
      assert.equal(await window.getByText('Stale reader result').count(), 0);
      await window.getByText('Collections refreshed. Search again for current results.').waitFor();
    }
    // Simulate the core refusing shutdown during a download. The main window
    // must remain available so the user can cancel or wait for the download.
    await app.evaluate(({ dialog }) => {
      globalThis.smokeOriginalFetch = globalThis.fetch;
      globalThis.smokeQuitError = '';
      globalThis.fetch = async (url, options) => new URL(url).pathname === '/api/quit'
        ? new Response('Wait for the current download to finish', { status: 409 })
        : globalThis.smokeOriginalFetch(url, options);
      dialog.showErrorBox = (_title, message) => { globalThis.smokeQuitError = message; };
    });
    await app.evaluate(({ BrowserWindow }, address) => {
      const main = BrowserWindow.getAllWindows().find(w => w.webContents.getURL() === address);
      if (!main) throw new Error('Main window not found');
      main.close();
    }, window.url());
    const quitError = await app.evaluate(async () => {
      for (let i = 0; i < 100 && !globalThis.smokeQuitError; i++) await new Promise(r => setTimeout(r, 50));
      return globalThis.smokeQuitError;
    });
    assert.match(quitError, /current download/);
    assert.equal(window.isClosed(), false);
    await app.evaluate(() => { globalThis.fetch = globalThis.smokeOriginalFetch; });
    await app.close(); app = null;
    // Select a library without the test-only root override, then restart and
    // verify that the saved filesystem identity reopens the same folder.
    const persistentEnv = { ...env, OFFLINE_LIBRARY_USER_DATA: path.join(temp, 'persistent-user-data') };
    delete persistentEnv.OFFLINE_LIBRARY_ROOT;
    app = await _electron.launch({ executablePath: executable, args, env: persistentEnv, chromiumSandbox, timeout: 60000 });
    const selection = await app.firstWindow();
    await selection.getByText('Advanced settings', { exact: true }).click();
    await selection.getByRole('button', { name: 'Choose a different folder' }).waitFor({ timeout: 30000 });
    await app.evaluate(({ dialog }, chosen) => { dialog.showOpenDialog = async () => ({ canceled: false, filePaths: [chosen] }); }, library);
    await selection.getByRole('button', { name: 'Choose a different folder' }).click();
    await selection.getByText(collisionFixture ? '2 collections available' : '1 collection available').waitFor({ timeout: 30000 });
    assert.equal(await selection.locator('#root').textContent(), library);
    await app.close(); app = null;
    app = await _electron.launch({ executablePath: executable, args, env: persistentEnv, chromiumSandbox, timeout: 60000 });
    const reopened = await app.firstWindow();
    await reopened.getByText(collisionFixture ? '2 collections available' : '1 collection available').waitFor({ timeout: 30000 });
    assert.equal(await reopened.locator('#root').textContent(), library);
    // Missing drives and legacy settings open setup, without a startup picker,
    // recreating the missing path, or replacing the user's saved selection.
    await app.close(); app = null;
    for (const savedLibrary of [library, path.join(temp, 'unplugged-drive', 'Offline Library')]) {
      const settingsFile = path.join(persistentEnv.OFFLINE_LIBRARY_USER_DATA, 'library-settings.json');
      const settings = JSON.stringify({ library: savedLibrary }) + '\n';
      fs.writeFileSync(settingsFile, settings);
      app = await _electron.launch({ executablePath: executable, args, env: persistentEnv, chromiumSandbox, timeout: 60000 });
      const recovery = await app.firstWindow();
      await recovery.getByText('Your previous library is unavailable. Reconnect its drive or choose a location below.').waitFor({ timeout: 30000 });
      assert.equal(await recovery.locator('[data-panel=location]').isVisible(), true);
      assert.equal(await recovery.getByRole('radio', { name: /This computer/ }).first().isVisible(), true);
      assert.equal(fs.readFileSync(settingsFile, 'utf8'), settings);
      assert.equal(fs.existsSync(path.join(temp, 'unplugged-drive')), false);
      await app.close(); app = null;
    }
    if (collisionFixture) {
      const collisionLibrary = path.join(temp, 'collision-library');
      const zimDir = path.join(collisionLibrary, 'content', 'zim');
      fs.mkdirSync(zimDir, { recursive: true });
      fs.copyFileSync(fixture, path.join(zimDir, 'café.zim'));
      fs.copyFileSync(collisionFixture, path.join(zimDir, 'cafe.zim'));
      app = await _electron.launch({ executablePath: executable, args, env: { ...env, OFFLINE_LIBRARY_ROOT: collisionLibrary }, chromiumSandbox, timeout: 60000 });
      const collision = await app.firstWindow();
      await collision.waitForFunction(()=>lastLibraryState!==null);
      await collision.evaluate(() => { document.querySelectorAll('[data-panel]').forEach(p => { p.hidden = false; }); });
      await collision.getByText('2 collections available').waitFor({ timeout: 30000 });
      await collision.locator('#reading-filters').evaluate(node=>node.open=true);
      await collision.getByRole('button', { name: 'Load collections' }).click();
      await collision.getByText('2 collections available for search.').waitFor({ timeout: 30000 });
      const labels = await collision.locator('#library-collection option').allTextContents();
      assert.equal(labels.filter(label => label.includes('shared reader address; search unavailable')).length, 2);
      for (const mode of ['titles', 'fulltext']) {
        await collision.locator('#library-mode').selectOption(mode);
        await collision.locator('#library-query').fill('vue');
        await collision.getByRole('button', { name: 'Search library' }).click();
        await collision.getByText(/0 of 2 collection\(s\) searched/).waitFor({ timeout: 30000 });
        assert.match(await collision.locator('#library-status').textContent(), /shared reader address/);
        assert.equal(await collision.locator('#library-results a').count(), 0);
      }
    } else {
      console.log('SKIP: real reader-address collision fixture not supplied');
    }
    // Selecting This computer uses a library in the user's home without a folder picker.
    if (app) { await app.close(); app = null; }
    const fakeHome = path.join(temp, 'test-home'); fs.mkdirSync(fakeHome);
    const homeEnv = { ...persistentEnv, OFFLINE_LIBRARY_USER_DATA: path.join(temp, 'home-choice-user-data') };
    app = await _electron.launch({ executablePath: executable, args, env: homeEnv, chromiumSandbox, timeout: 60000 });
    const homeWindow = await app.firstWindow();
    await homeWindow.waitForFunction(() => lastLibraryState !== null, { timeout: 30000 });
    assert.equal(await homeWindow.locator('#library-home').isVisible(),false);
    assert.equal(await homeWindow.locator('#manage-library').isVisible(),false);
    await homeWindow.evaluate(()=>showSetupStep('read'));
    assert.equal(await homeWindow.locator('[data-panel=read]').isVisible(),false);
    assert.equal(await homeWindow.locator('[data-panel=location]').isVisible(),true);

    await app.evaluate(({ app }, home) => { const original = app.getPath.bind(app); app.getPath = key => key === 'home' ? home : original(key); }, fakeHome);
    await homeWindow.getByRole('button', {name:'2 Collections',exact:true}).click();
    await homeWindow.locator('#collection-preset').selectOption('compact');
    await homeWindow.getByRole('button', {name:'1 Location',exact:true}).click();
    await homeWindow.getByRole('radio', { name: /This computer/ }).first().check();
    await homeWindow.waitForFunction(expected => document.querySelector('#root').textContent === expected, path.join(fakeHome, 'Offline Library'), { timeout: 30000 });
    assert.equal(fs.statSync(path.join(fakeHome, 'Offline Library')).isDirectory(), true);
    assert.equal(await homeWindow.getByRole('radio', { name: /This computer/ }).first().isChecked(), true);
    await homeWindow.waitForFunction(()=>document.querySelector('#collection-preset').value==='compact');
    assert.equal(await homeWindow.locator('#curated-collections input:checked').count(),5);
    await homeWindow.getByRole('radio',{name:/Another drive or card/}).check();
    assert.equal(await homeWindow.getByRole('radio',{name:/Download to a fast drive first/}).isChecked(),true);
    assert.equal(await homeWindow.locator('#stage-destination-area').isVisible(),true);
    console.log('PASS: bundled window, offline search, homepage, manual, reader, safe close and library identity');
  } finally {
    if (app) {
      await app.evaluate(() => { if (globalThis.smokeOriginalFetch) globalThis.fetch = globalThis.smokeOriginalFetch; }).catch(() => {});
      await app.close();
    }
    fs.rmSync(temp, { recursive: true, force: true });
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
