'use strict';
const { app, BrowserWindow, dialog, ipcMain, session } = require('electron');
const { spawn } = require('node:child_process');
const { mkdir, readFile, writeFile, rename } = require('node:fs/promises');
const path = require('node:path');
const { isLocal, parseCoreURL } = require('./policy.cjs');
const { identifyLibrary, sameLibrary, resolveStartupSelection, prepareDriveLibrary } = require('./library-selection.cjs');

let mainWindow, core, coreAddress, permitQuit = false, switching = false, pendingQuit = false, destinationMode = '', stagingStrategy = 'staged', stagingSource = 'system', stagingDestination = '', stagingDestinationReserve = 2, libraryDriveChoice = '', destinationDriveChoice = '';
let activeLibrary, startupNotice = '', collectionSelection = {mode:'auto',ids:[]};
if (process.env.OFFLINE_LIBRARY_USER_DATA) {
  app.setPath('userData', path.resolve(process.env.OFFLINE_LIBRARY_USER_DATA));
}
const executable = process.platform === 'win32' ? 'offline-library-core.exe' : 'offline-library-core';
const readerExecutable = process.platform === 'win32' ? 'kiwix-serve.exe' : 'kiwix-serve';
function binaryPath(name) { return path.join(process.resourcesPath, 'bin', name); }
function settingsPath() { return path.join(app.getPath('userData'), 'library-settings.json'); }
async function librarySelection() {
  if (process.env.OFFLINE_LIBRARY_ROOT) return { directory: path.resolve(process.env.OFFLINE_LIBRARY_ROOT), explicit: true, override: true };
  try {
    const data = JSON.parse(await readFile(settingsPath(), 'utf8'));
    if (typeof data.library === 'string' && path.isAbsolute(data.library)) return { directory: data.library, explicit: true, identity: data.identity };
  } catch {}
  return { directory: path.join(app.getPath('userData'), 'library'), explicit: false };
}
async function saveLibrary(directory, expected) {
  const identity = await identifyLibrary(directory);
  if (expected && !sameLibrary(identity, expected)) throw new Error('The library folder changed. Choose it again.');
  await mkdir(app.getPath('userData'), { recursive: true });
  const temporary = settingsPath() + '.part';
  await writeFile(temporary, JSON.stringify({ library: directory, identity }) + '\n', { mode: 0o600 });
  await rename(temporary, settingsPath());
}
async function startupLibrary() {
  const selection = await librarySelection();
  const directory = await resolveStartupSelection(selection, async () => {
    startupNotice = 'Your previous library is unavailable. Reconnect its drive or choose a location below.';
    // Run the setup UI from app storage. Do not recreate an unplugged library
    // or replace its saved selection before the user chooses a location.
    const fallback = path.join(app.getPath('userData'), 'setup-library');
    await mkdir(fallback, { recursive: true });
    return fallback;
  });
  if (directory && !selection.explicit) await saveLibrary(directory, await identifyLibrary(directory));
  activeLibrary = { directory, identity: await identifyLibrary(directory) };
  return directory;
}
function startCore(directory) {
  return new Promise((resolve, reject) => {
    let done = false, buffer = '';
    const program = process.env.OFFLINE_LIBRARY_CORE || binaryPath(executable);
    const reader = process.env.OFFLINE_LIBRARY_READER || binaryPath(readerExecutable);
    const child = spawn(program, ['--no-browser', '--library', directory, '--reader', reader], { windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    core = child; child.intentional = false;
    const timer = setTimeout(() => { if (!done) { done = true; child.intentional = true; child.kill(); reject(new Error('The library service did not start.')); } }, 15000);
    child.once('error', error => { if (!done) { done = true; clearTimeout(timer); reject(error); } });
    child.stdout.on('data', chunk => {
      if (done) return;
      buffer += chunk.toString('utf8');
      if (buffer.length > 8192) { done = true; clearTimeout(timer); child.intentional = true; child.kill(); reject(new Error('Invalid library service response.')); return; }
      const end = buffer.indexOf('\n'); if (end < 0) return;
      try { coreAddress = parseCoreURL(buffer.slice(0, end)); done = true; clearTimeout(timer); resolve(coreAddress); }
      catch (error) { done = true; clearTimeout(timer); child.intentional = true; child.kill(); reject(error); }
    });
    child.stderr.on('data', () => {}); // Drain without exposing local paths or access tokens.
    child.once('exit', (code, signal) => {
      if (core === child) { core = null; coreAddress = null; }
      if (!done) { done = true; clearTimeout(timer); reject(new Error(`The library service exited (${code ?? signal}).`)); }
      else if (!child.intentional && !switching) { if (code !== 0) dialog.showErrorBox('Offline Library', 'The library service stopped unexpectedly.'); permitQuit = true; app.quit(); }
    });
  });
}
async function stopCore() {
  if (!core || !coreAddress) return;
  const child = core, parsed = new URL(coreAddress), token = parsed.hash.slice(1);
  const endpoint = new URL('/api/quit', parsed);
  let response;
  try { response = await fetch(endpoint, { method: 'POST', headers: { 'X-Library-Token': token }, signal: AbortSignal.timeout(5000) }); }
  catch { throw new Error('Could not safely stop the library service.'); }
  if (!response.ok) throw new Error((await response.text()).trim() || 'Could not stop the library service.');
  child.intentional = true;
  await new Promise((resolve, reject) => {
    if (child.exitCode !== null || child.signalCode !== null) { resolve(); return; }
    const timer = setTimeout(() => reject(new Error('The library service did not stop.')), 5000);
    child.once('exit', () => { clearTimeout(timer); resolve(); });
  });
}
function configureWindow(window) {
  window.webContents.setWindowOpenHandler(({ url }) => ({ action: isLocal(url) ? 'allow' : 'deny', overrideBrowserWindowOptions: { webPreferences: { nodeIntegration: false, contextIsolation: true, sandbox: true, preload: undefined } } }));
  window.webContents.on('will-navigate', (event, url) => { if (!isLocal(url)) event.preventDefault(); });
  window.webContents.on('will-redirect', (event, url) => { if (!isLocal(url)) event.preventDefault(); });
  window.webContents.on('did-create-window', child => configureWindow(child));
}
async function run() {
  if (!app.requestSingleInstanceLock()) { permitQuit = true; app.quit(); return; }
  app.on('second-instance', () => { if (mainWindow) { if (mainWindow.isMinimized()) mainWindow.restore(); mainWindow.focus(); } });
  await app.whenReady();
  session.defaultSession.webRequest.onBeforeRequest((details, callback) => {
    const address = details.url;
    callback({ cancel: !isLocal(address) && !address.startsWith('data:') && !address.startsWith('blob:') });
  });
  ipcMain.handle('get-destination-mode', event => event.sender === mainWindow?.webContents ? destinationMode : '');
  ipcMain.handle('set-destination-mode', (event, mode) => { if (event.sender === mainWindow?.webContents && ['local', 'external'].includes(mode)) destinationMode = mode; });
  ipcMain.handle('get-staging-options', event => event.sender === mainWindow?.webContents ? { mode: destinationMode, strategy: stagingStrategy, source: stagingSource, destination: stagingDestination, reserveGiB: stagingDestinationReserve, libraryDrive: libraryDriveChoice, destinationDrive: destinationDriveChoice, startupNotice, collectionSelection } : {});
  ipcMain.handle('set-collection-selection', (event, selection) => {
    if (event.sender !== mainWindow?.webContents || !selection || !['auto','compact','256','512','custom'].includes(selection.mode) || !Array.isArray(selection.ids) || selection.ids.length>50 || selection.ids.some(id=>typeof id!=='string'||!/^[a-f0-9-]{36}$/i.test(id))) return;
    collectionSelection={mode:selection.mode,ids:[...new Set(selection.ids)]};
  });
  ipcMain.handle('set-staging-strategy', (event, strategy) => { if (event.sender === mainWindow?.webContents && ['direct', 'staged'].includes(strategy)) stagingStrategy = strategy; });
  ipcMain.handle('set-staging-reserve', (event, reserve) => { if (event.sender === mainWindow?.webContents && Number.isInteger(reserve) && reserve >= 2 && reserve <= 1000000) stagingDestinationReserve = reserve; });
  ipcMain.handle('set-staging-source', (event, source) => { if (event.sender === mainWindow?.webContents && ['system', 'other'].includes(source)) stagingSource = source; });
  ipcMain.handle('choose-stage-destination', async (event, suggestedPath) => {
    if (!mainWindow || event.sender !== mainWindow.webContents || switching) return { error: 'Library window is unavailable.' };
    const chosen = await dialog.showOpenDialog(mainWindow, { title: 'Choose the final library folder', ...(typeof suggestedPath === 'string' && path.isAbsolute(suggestedPath) ? {defaultPath:suggestedPath} : {}), properties: ['openDirectory', 'createDirectory'] });
    if (chosen.canceled || chosen.filePaths.length !== 1) return { canceled: true };
    stagingDestination = chosen.filePaths[0]; destinationDriveChoice = 'custom'; return { path: stagingDestination };
  });
  async function switchLibrary(directory, prepare) {
    if (switching) return { error: 'Library window is unavailable.' };
    switching = true;
    const previousSelection = activeLibrary;
    const previousNotice = startupNotice;
    const previous = previousSelection.directory;
    let previousIdentity = previousSelection.identity;
    if (!previousIdentity) { try { previousIdentity = await identifyLibrary(previous); } catch {} }
    let stopped = false;
    try {
      await stopCore(); stopped = true;
      if (prepare) await prepare();
      const identity = await identifyLibrary(directory);
      const address = await startCore(directory);
      startupNotice = '';
      if (!mainWindow.isDestroyed()) await mainWindow.loadURL(address);
      await saveLibrary(directory, identity);
      activeLibrary = { directory, identity }; startupNotice = '';
      return { changed: true };
    } catch (error) {
      startupNotice = previousNotice;
      if (stopped) {
        try {
          await stopCore();
          if (!previousIdentity || !sameLibrary(await identifyLibrary(previous), previousIdentity)) throw new Error('Previous library is unavailable.');
          await startCore(previous);
          if (!mainWindow.isDestroyed()) await mainWindow.loadURL(coreAddress);
        } catch { /* Keep the original error visible. */ }
      }
      return { error: error.message };
    } finally {
      switching = false;
      if (pendingQuit) { pendingQuit = false; app.quit(); }
    }
  }
  async function currentDrives() {
    if (!coreAddress) throw new Error('Library service is unavailable.');
    const address = new URL(coreAddress), token = address.hash.slice(1);
    const response = await fetch(new URL('/api/drives', address), {headers:{'X-Library-Token':token}});
    if (!response.ok) throw new Error('Could not refresh drives.');
    return (await response.json()).drives;
  }
  async function driveBase(drive) {
    if (drive === 'home') return app.getPath('home');
    if (typeof drive !== 'string' || !path.isAbsolute(drive)) throw new Error('Choose a drive first.');
    const drives = await currentDrives();
    if (!drives.some(item => item.path === drive)) throw new Error('The drive is no longer available. Refresh the list.');
    const home = app.getPath('home');
    const root = path.parse(home).root;
    const sameRoot = process.platform === 'win32' ? path.resolve(drive).toLowerCase() === root.toLowerCase() : path.resolve(drive) === root;
    return sameRoot ? home : drive;
  }
  ipcMain.handle('use-library-drive', async (event, drive) => {
    if (!mainWindow || event.sender !== mainWindow.webContents || switching) return { error:'Library window is unavailable.' };
    try {
      const base = await driveBase(drive), identity = await identifyLibrary(base), directory = path.join(base, 'Offline Library');
      const current = activeLibrary;
      if (path.resolve(current.directory) === path.resolve(directory)) { libraryDriveChoice=drive; return { unchanged:true }; }
      const previousDrive=libraryDriveChoice;libraryDriveChoice=drive;const result=await switchLibrary(directory, () => prepareDriveLibrary(base, identity));if(result.error)libraryDriveChoice=previousDrive;return result;
    } catch (error) { return { error:error.message }; }
  });
  ipcMain.handle('use-stage-drive', async (event, drive) => {
    if (!mainWindow || event.sender !== mainWindow.webContents || switching) return { error:'Library window is unavailable.' };
    try {
      const base = await driveBase(drive), identity = await identifyLibrary(base);
      const address = new URL(coreAddress), response = await fetch(new URL('/api/state',address),{headers:{'X-Library-Token':address.hash.slice(1)}});
      if (!response.ok) throw new Error('Cannot check current operation.');
      const state = await response.json(); if (state.queueActive || ['waiting','retrying','checking','resolving','downloading','verifying','discarding','repairing','indexing'].includes(state.job?.state)) throw new Error('Wait for the current operation to finish.');
      const validation=await fetch(new URL('/api/stage-validate',address),{method:'POST',headers:{'X-Library-Token':address.hash.slice(1),'Content-Type':'application/json'},body:JSON.stringify({destination:base})});
      if(!validation.ok)throw new Error((await validation.text()).trim());const details=await validation.json();if(details.sameFilesystem)throw new Error('Choose a different drive for staging.');
      stagingDestination = await prepareDriveLibrary(base, identity); destinationDriveChoice=drive; return {path:stagingDestination};
    } catch (error) { return {error:error.message}; }
  });
  ipcMain.handle('choose-library', async (event, suggestedPath) => {
    if (!mainWindow || event.sender !== mainWindow.webContents || switching) return { error:'Library window is unavailable.' };
    const chosen = await dialog.showOpenDialog(mainWindow, {title:'Choose a library folder',...(typeof suggestedPath==='string' && path.isAbsolute(suggestedPath)?{defaultPath:suggestedPath}:{}),properties:['openDirectory','createDirectory']});
    if (chosen.canceled || chosen.filePaths.length !== 1) return {canceled:true};
    const previousDrive=libraryDriveChoice;libraryDriveChoice='custom';const result=await switchLibrary(chosen.filePaths[0]);if(result.error)libraryDriveChoice=previousDrive;return result;
  });
  const directory = await startupLibrary();
  if (!directory) { permitQuit = true; app.quit(); return; }
  const address = await startCore(directory);
  mainWindow = new BrowserWindow({ title: 'Offline Library', width: 1120, height: 850, minWidth: 520, minHeight: 500, show: false, webPreferences: { preload: path.join(__dirname, 'preload.cjs'), nodeIntegration: false, contextIsolation: true, sandbox: true } });
  // Keep the controls available if the core refuses to stop (for example,
  // while a download is active). Quit only after the core stops cleanly.
  mainWindow.on('close', event => { if (!permitQuit) { event.preventDefault(); app.quit(); } });
  configureWindow(mainWindow); mainWindow.once('ready-to-show', () => mainWindow.show()); await mainWindow.loadURL(address);
}
app.on('window-all-closed', () => app.quit());
app.on('before-quit', event => {
  if (permitQuit || !core) return;
  event.preventDefault();
  if (switching) { pendingQuit = true; return; }
  switching = true;
  stopCore().then(() => { permitQuit = true; app.quit(); }).catch(error => { switching = false; dialog.showErrorBox('Offline Library', error.message); });
});
run().catch(error => { dialog.showErrorBox('Offline Library', error.message); permitQuit = true; if (core) { core.intentional = true; core.kill(); } app.quit(); });
