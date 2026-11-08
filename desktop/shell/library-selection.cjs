'use strict';
const { stat, lstat, mkdir } = require('node:fs/promises');
const path = require('node:path');

async function identifyLibrary(directory) {
  const info = await stat(directory, { bigint: true });
  if (!info.isDirectory()) throw new Error('The library folder is not a directory.');
  return { device: info.dev.toString(), inode: info.ino.toString() };
}
function sameLibrary(first, second) {
  return !!first && !!second && typeof first.device === 'string' && typeof first.inode === 'string' &&
    first.device === second.device && first.inode === second.inode;
}
async function resolveStartupSelection(selection, chooseReplacement) {
  if (!selection.explicit) { await mkdir(selection.directory, { recursive: true }); return selection.directory; }
  let current;
  try { current = await identifyLibrary(selection.directory); } catch {}
  if (current && (selection.override || sameLibrary(current, selection.identity))) return selection.directory;
  if (selection.override) throw new Error('The selected library folder is missing or unavailable.');
  return chooseReplacement();
}
async function prepareDriveLibrary(base, expected) {
  if (!sameLibrary(await identifyLibrary(base), expected)) throw new Error('The selected drive changed. Choose it again.');
  const directory = path.join(base, 'Offline Library');
  try { await mkdir(directory); } catch (error) { if (error.code !== 'EEXIST') throw error; }
  const info = await lstat(directory);
  if (!info.isDirectory() || info.isSymbolicLink()) throw new Error('Offline Library exists but is not a normal folder.');
  if (!sameLibrary(await identifyLibrary(base), expected)) throw new Error('The selected drive changed. Choose it again.');
  return directory;
}
module.exports = { identifyLibrary, sameLibrary, resolveStartupSelection, prepareDriveLibrary };
