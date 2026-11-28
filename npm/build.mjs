import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const web = join(root, 'web');
const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const tag = `v${manifest.version}`;
const assetsDir = join(root, 'npm', 'dist', tag);
const metadataPath = join(root, 'npm', 'release.json');
const targets = manifest.os.flatMap(os => manifest.cpu.map(cpu => {
  const platform = `${os}-${cpu}`;
  const extension = os === 'win32' ? '.exe' : '';
  return {
    platform,
    file: `gilir-${platform}${extension}`,
    goOS: os === 'win32' ? 'windows' : os,
    goArch: cpu === 'x64' ? 'amd64' : cpu,
  };
}));

function checksum(file) {
  return createHash('sha256').update(readFileSync(file)).digest('hex');
}

function checksumManifest(assets) {
  return Object.values(assets)
    .map(({ file, sha256 }) => `${sha256}  ${file}\n`)
    .join('');
}

if (process.argv.includes('--check')) {
  const release = existsSync(metadataPath) && JSON.parse(readFileSync(metadataPath, 'utf8'));
  const sums = join(assetsDir, 'SHA256SUMS');
  const ready = release?.version === manifest.version && targets.every(({ platform, file }) => {
    const asset = join(assetsDir, file);
    const entry = release.assets?.[platform];
    return entry?.file === file && existsSync(asset) && entry.sha256 === checksum(asset);
  }) && existsSync(sums) && readFileSync(sums, 'utf8') === checksumManifest(release.assets);
  if (!ready) {
    console.error(`gilir: missing or stale ${tag} release assets; run npm run build:release`);
    process.exitCode = 1;
  }
} else {
  rmSync(metadataPath, { force: true });
  rmSync(join(assetsDir, 'SHA256SUMS'), { force: true });
  execFileSync('pnpm', ['install', '--frozen-lockfile', '--prod=false'], { cwd: web, stdio: 'inherit' });
  execFileSync('pnpm', ['build'], { cwd: web, stdio: 'inherit' });

  mkdirSync(assetsDir, { recursive: true });
  const assets = {};
  for (const { platform, file, goOS, goArch } of targets) {
    const destination = join(assetsDir, file);
    execFileSync('go', [
      'build', '-tags=embedweb', '-trimpath',
      `-ldflags=-s -w -X github.com/armedi/gilir/internal/cli.version=${manifest.version}`,
      '-o', destination, './cmd/gilir',
    ], {
      cwd: root,
      stdio: 'inherit',
      env: { ...process.env, CGO_ENABLED: '0', GOOS: goOS, GOARCH: goArch },
    });
    assets[platform] = { file, sha256: checksum(destination) };
    // Guard the -X path: a stale package path in ldflags would leave the
    // stamp silently empty and every release would report "dev".
    if (platform === `${process.platform}-${process.arch}`) {
      const reported = execFileSync(destination, ['--version'], { encoding: 'utf8' }).trim();
      if (reported !== `gilir ${manifest.version}`) {
        throw new Error(`built ${file} reports ${JSON.stringify(reported)}, want "gilir ${manifest.version}"`);
      }
    }
  }
  writeFileSync(join(assetsDir, 'SHA256SUMS'), checksumManifest(assets));
  writeFileSync(metadataPath, `${JSON.stringify({ version: manifest.version, assets }, null, 2)}\n`);
}
