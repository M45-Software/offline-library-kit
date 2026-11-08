'use strict';
// Optional end-to-end check against already staged, exact-version public sources.
const {_electron}=require('playwright-core');
const fs=require('node:fs'),path=require('node:path'),os=require('node:os'),assert=require('node:assert/strict');
(async()=>{
 const executable=process.env.OFFLINE_LIBRARY_SMOKE_APP,sources=process.env.OLK_SEARCH_REAL_SOURCES;
 if(!executable||!sources)throw Error('Set OFFLINE_LIBRARY_SMOKE_APP and OLK_SEARCH_REAL_SOURCES');
 const manifest=JSON.parse(fs.readFileSync(path.join(__dirname,'../../../search-indexes/catalog/manifest.json'),'utf8'));
 const temp=fs.mkdtempSync(path.join(os.tmpdir(),'olk-search-real-ui-')),root=path.join(temp,'library'),dir=path.join(root,'content','zim');fs.mkdirSync(dir,{recursive:true});
 let app;
 try{
  for(const archive of manifest.Archives)fs.symlinkSync(path.join(sources,archive.Collection+'.zim'),path.join(dir,archive.Collection+'.zim'));
  app=await _electron.launch({executablePath:executable,env:{...process.env,OFFLINE_LIBRARY_ROOT:root,OFFLINE_LIBRARY_USER_DATA:path.join(temp,'app-data')},chromiumSandbox:process.platform==='linux',timeout:60000});
  const page=await app.firstWindow();await page.waitForFunction(n=>lastLibraryState?.archiveCount===n,manifest.Archives.length,{timeout:60000});
  await page.locator('[data-step=read]').click();await page.locator('#load-collections').click();await page.getByText('33 collections available for search.',{exact:true}).waitFor({timeout:60000});
  const checks=[];
  async function search(id,mode,q){
   await page.locator('#library-collection').selectOption(id);await page.locator('#library-mode').selectOption(mode);await page.locator('#library-query').fill(q);
   const pending=page.waitForResponse(r=>r.url().includes('/api/library/')&&r.url().includes('q='+encodeURIComponent(q).replace(/%20/g,'+')),{timeout:60000});
   await page.getByRole('button',{name:'Search library',exact:true}).click();const response=await pending;assert.equal(response.status(),200);const result=await response.json();assert(result.hybrid);assert(result.results.length>0);assert.equal(result.unavailable.length,0);
   await page.locator('#library-results a').first().waitFor({timeout:60000});const link=await page.locator('#library-results a').first().getAttribute('href');const head=await page.request.head(link);assert.equal(head.status(),200);
   checks.push({mode,query:q,shown:result.results.length,total:result.total});
  }
  await search('438c7985-7b24-b470-9dfc-7b0cafa02269','hybrid','fermentation');
  await search('97b9c3e1-51db-324d-9608-0da874fc7ab0','hybrid','battery');
  await search('97b9c3e1-51db-324d-9608-0da874fc7ab0','text','battery replacement');
  await search('97b9c3e1-51db-324d-9608-0da874fc7ab0','titles','battery replacement');
  await search('','hybrid','solar panel');
  await search('','catalog','cheese');
  if(process.env.OLK_SEARCH_UI_SCREENSHOT)await page.screenshot({path:process.env.OLK_SEARCH_UI_SCREENSHOT,fullPage:true});
  console.log(JSON.stringify({passed:true,collections:manifest.Archives.length,checks},null,2));
 }finally{if(app)await app.close();fs.rmSync(temp,{recursive:true,force:true})}
})().catch(error=>{console.error(error);process.exitCode=1});
