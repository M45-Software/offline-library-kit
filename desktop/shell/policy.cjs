'use strict';
function isLocal(address) {
  try {
    const parsed = new URL(address);
    return parsed.protocol === 'about:' && parsed.href === 'about:blank' ||
      parsed.protocol === 'http:' && parsed.hostname === '127.0.0.1' && parsed.username === '' && parsed.password === '';
  } catch { return false; }
}
function parseCoreURL(line) {
  const prefix = 'Offline Library control panel: ';
  if (!line.startsWith(prefix)) throw new Error('Application did not report its control panel.');
  const address = line.slice(prefix.length).trim();
  const parsed = new URL(address);
  if (!isLocal(address) || !parsed.port || parsed.pathname !== '/' || !/^#[a-f0-9]{64}$/.test(parsed.hash)) {
    throw new Error('Application reported an invalid control panel address.');
  }
  return address;
}
module.exports = { isLocal, parseCoreURL };
