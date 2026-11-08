'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { identifyLibrary, sameLibrary, resolveStartupSelection, prepareDriveLibrary } = require('../library-selection.cjs');

test('recognizes a replaced or missing selected folder', async () => {
  const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'library-selection-'));
  try {
    const folder = path.join(temp, 'library');
    await fs.mkdir(folder);
    const original = await identifyLibrary(folder);
    assert.equal(sameLibrary(original, await identifyLibrary(folder)), true);
    await fs.rename(folder, path.join(temp, 'old'));
    await assert.rejects(identifyLibrary(folder));
    await fs.mkdir(folder);
    assert.equal(sameLibrary(original, await identifyLibrary(folder)), false);
    assert.equal(sameLibrary(null, original), false);
  } finally { await fs.rm(temp, { recursive: true, force: true }); }
});

test('startup never recreates a missing saved folder', async () => {
  const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'library-startup-'));
  try {
    const missing = path.join(temp, 'unplugged', 'library');
    let prompts = 0;
    const selected = await resolveStartupSelection({ directory: missing, explicit: true, identity: { device: '1', inode: '2' } }, async () => { prompts++; return null; });
    assert.equal(selected, null); assert.equal(prompts, 1);
    await assert.rejects(fs.stat(path.join(temp, 'unplugged')));
    const fallback = path.join(temp, 'app-data', 'library');
    assert.equal(await resolveStartupSelection({ directory: fallback, explicit: false }, async () => { throw Error('unexpected prompt'); }), fallback);
    assert.equal((await fs.stat(fallback)).isDirectory(), true);
    let confirmed = false;
    await resolveStartupSelection({ directory: fallback, explicit: true }, async () => { confirmed = true; return null; });
    assert.equal(confirmed, true); // Legacy settings require an explicit re-selection.
  } finally { await fs.rm(temp, { recursive: true, force: true }); }
});


test('creates a library only on the selected, unchanged drive', async () => {
  const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'library-drive-'));
  try {
    const identity = await identifyLibrary(temp);
    const folder = await prepareDriveLibrary(temp, identity);
    assert.equal(folder, path.join(temp, 'Offline Library'));
    assert.equal((await fs.stat(folder)).isDirectory(), true);
    assert.equal(await prepareDriveLibrary(temp, identity), folder);
    await assert.rejects(prepareDriveLibrary(temp, {device:'invalid', inode:'invalid'}), /changed/);
    await fs.rmdir(folder); await fs.writeFile(folder, 'existing file');
    await assert.rejects(prepareDriveLibrary(temp, identity), /not a normal folder/);
    assert.equal(await fs.readFile(folder, 'utf8'), 'existing file');
  } finally { await fs.rm(temp, { recursive: true, force: true }); }
});
