const { createHash } = require('node:crypto');
const { existsSync } = require('node:fs');
const { mkdir, mkdtemp, rename, rm, writeFile } = require('node:fs/promises');
const { homedir } = require('node:os');
const { join } = require('node:path');

async function getBinary() {
  const { version, assets } = require('./release.json');
  const platform = `${process.platform}-${process.arch}`;
  const asset = assets[platform];
  if (!asset) throw new Error(`unsupported platform ${platform}`);

  const { file, sha256 } = asset;
  const cacheRoot = process.platform === 'win32'
    ? process.env.LOCALAPPDATA || join(homedir(), 'AppData', 'Local')
    : process.env.XDG_CACHE_HOME || join(homedir(), '.cache');
  const directory = join(cacheRoot, 'gilir', version, sha256);
  const binary = join(directory, file);
  if (existsSync(binary)) return binary;

  const url = `https://github.com/armedi/gilir/releases/download/v${version}/${file}`;
  const response = await fetch(url, { signal: AbortSignal.timeout(60_000) });
  if (!response.ok) throw new Error(`could not download ${file}: GitHub returned HTTP ${response.status}`);
  const bytes = Buffer.from(await response.arrayBuffer());
  const actual = createHash('sha256').update(bytes).digest('hex');
  if (actual !== sha256) throw new Error(`checksum mismatch for ${file}`);

  await mkdir(directory, { recursive: true, mode: 0o700 });
  const staging = await mkdtemp(join(directory, 'download-'));
  try {
    const stagedBinary = join(staging, file);
    await writeFile(stagedBinary, bytes, { mode: 0o700 });
    try {
      await rename(stagedBinary, binary);
    } catch (error) {
      // Another launch may have installed the same verified binary first.
      if (!existsSync(binary)) throw error;
    }
  } finally {
    await rm(staging, { recursive: true, force: true });
  }
  return binary;
}

if (require.main === module) {
  getBinary().catch(error => {
    console.warn(`gilir: ${error.message}; will retry on first run`);
  });
}

module.exports = { getBinary };
