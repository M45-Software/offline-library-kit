#!/usr/bin/env node
// Create and sign a separate macOS preview folder; never upload or notarize it.
import fs from 'node:fs/promises';
import { createReadStream } from 'node:fs';
import { createHash } from 'node:crypto';
import { execFile } from 'node:child_process';
import path from 'node:path';
import { promisify } from 'node:util';
import { sign } from '@electron/osx-sign';

const exec = promisify(execFile);
function usage() { throw Error('Usage: node desktop/signing/sign-copy.mjs <source-folder> <new-output-folder> --adhoc | --identity "Developer ID Application: …"'); }
function nested(parent, child) { const rel = path.relative(parent, child); return rel === '' || (!rel.startsWith('..' + path.sep) && rel !== '..' && !path.isAbsolute(rel)); }
async function digest(file) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(file)) hash.update(chunk);
  return hash.digest('hex');
}
async function checkHashes(dir, hashes) {
  if (!hashes || typeof hashes !== 'object' || Array.isArray(hashes) || !Object.hasOwn(hashes, 'kiwix-serve') || !Object.hasOwn(hashes, 'offline-library-core')) throw Error('Missing reader or core hash');
  for (const [name, expected] of Object.entries(hashes)) {
    if (path.basename(name) !== name || !/^[a-f0-9]{64}$/.test(expected)) throw Error('Invalid binary hash record');
    const file = path.join(dir, name);
    if (!(await fs.lstat(file)).isFile() || await digest(file) !== expected) throw Error('Input binary differs from its build record: ' + name);
  }
}
async function main() {
  if (process.platform !== 'darwin') throw Error('Signing must run on macOS');
  const args = process.argv.slice(2);
  if (args.length !== 3 && args.length !== 4) usage();
  const adhoc = args[2] === '--adhoc' && args.length === 3;
  const developerID = args[2] === '--identity' && args.length === 4 && (/^Developer ID Application: .+ \([A-Z0-9]{10}\)$/.test(args[3]) || /^[a-fA-F0-9]{40}$/.test(args[3]));
  if (!adhoc && !developerID) usage();
  const source = await fs.realpath(args[0]);
  const outputArgument = path.resolve(args[1]);
  const output = path.join(await fs.realpath(path.dirname(outputArgument)), path.basename(outputArgument));
  if (nested(source, output) || nested(output, source)) throw Error('Source and output must be separate folders');
  const infoPath = path.join(source, 'build-info.json');
  const info = JSON.parse(await fs.readFile(infoPath, 'utf8'));
  if (!['darwin-amd64', 'darwin-arm64'].includes(info.target)) throw Error('Input is not a macOS preview');
  if (info.signing) throw Error('Use an unsigned build as the signing input');
  const inputApp = path.join(source, 'Offline Library.app');
  const inputBin = path.join(inputApp, 'Contents', 'Resources', 'bin');
  await checkHashes(inputBin, info.binary_sha256);
  await fs.mkdir(output); // Exclusive: preserve every existing folder.
  const marker = path.join(output, 'SIGNING-INCOMPLETE');
  await fs.writeFile(marker, 'Signing did not complete. Inspect or remove this new folder before retrying.\n', { flag: 'wx' });
  await exec('/usr/bin/ditto', [source, output], { maxBuffer: 4 * 1024 * 1024 });
  const app = path.join(output, 'Offline Library.app');
  const bin = path.join(app, 'Contents', 'Resources', 'bin');
  await checkHashes(bin, info.binary_sha256);
  await sign({ app, platform: 'darwin', identity: adhoc ? '-' : args[3], identityValidation: !adhoc,
    preAutoEntitlements: false, optionsForFile: adhoc ? () => ({ timestamp: 'none' }) : undefined });
  await exec('/usr/bin/codesign', ['--verify', '--deep', '--strict', '--verbose=2', app], { maxBuffer: 4 * 1024 * 1024 });
  for (const name of ['kiwix-serve', 'offline-library-core']) await exec('/usr/bin/codesign', ['--verify', '--strict', path.join(bin, name)]);
  info.pre_signing_binary_sha256 = info.binary_sha256;
  info.binary_sha256 = {};
  for (const name of Object.keys(info.pre_signing_binary_sha256)) info.binary_sha256[name] = await digest(path.join(bin, name));
  if (info.reader_source?.status === 'supplied and file hashes checked') info.reader_source.status = 'supplied and file hashes checked before signing';
  info.signing = { kind: adhoc ? 'ad-hoc' : 'certificate', notarized: false,
    note: 'Local signing only; no notarization or redistribution has been performed. Reader provenance hashes describe pre-signing inputs.' };
  await fs.writeFile(path.join(output, 'build-info.json'), JSON.stringify(info, null, 2) + '\n');
  await fs.unlink(marker);
  console.log(`Signed local copy: ${output}\nNot notarized; review source and licenses before redistribution.`);
}
main().catch(error => { console.error('Signing failed:', error.message); process.exitCode = 1; });
