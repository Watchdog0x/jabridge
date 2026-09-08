import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {mkdirSync, readFileSync, writeFileSync} from 'node:fs';
import {basename, join, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

export function rcVersion(tag) {
  const match = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)-rc\.(0|[1-9]\d*)$/.exec(tag ?? '');
  return match && match.slice(1).map(BigInt);
}

function compare(a, b) {
  const av = rcVersion(a.tag_name), bv = rcVersion(b.tag_name);
  for (let i = 0; i < 4; i++) if (av[i] !== bv[i]) return av[i] < bv[i] ? -1 : 1;
  return 0;
}

export function planPrune(releases, tag) {
  if (!rcVersion(tag)) return {skip: 'Not an RC release', targets: []};
  const published = releases.filter(r => rcVersion(r.tag_name) && !r.draft).sort(compare);
  const keep = published.at(-1);
  if (!keep || keep.tag_name !== tag) return {skip: 'This is not the newest published RC', targets: []};
  if (!keep.prerelease) throw Error('Kept RC is not marked as a prerelease');
  const archive = `jabridge_${tag.slice(1)}_linux_amd64.tar.gz`;
  for (const name of [archive, `${archive}.sha256`, `${archive}.sig`]) {
    const asset = keep.assets.find(a => a.name === name);
    if (!asset || asset.size <= 0 || !/^sha256:[0-9a-f]{64}$/.test(asset.digest ?? '')) throw Error('Kept RC assets are incomplete');
  }
  const targets = releases.filter(r => rcVersion(r.tag_name) && (r.prerelease || r.draft) && compare(r, keep) < 0);
  return {keep, targets};
}

function stamp(release) {
  return JSON.stringify({id: release.id, tag: release.tag_name, draft: release.draft, prerelease: release.prerelease,
    updated: release.updated_at, assets: [...release.assets].map(a => ({id:a.id,name:a.name,size:a.size,digest:a.digest})).sort((a,b)=>a.id-b.id)});
}

function gh(args) { return execFileSync('gh', args, {encoding:'utf8', maxBuffer:32<<20}); }
function api(repo, endpoint) { return JSON.parse(gh(['api', `repos/${repo}/${endpoint}`])); }
function list(repo) { return JSON.parse(gh(['api','--paginate','--slurp',`repos/${repo}/releases?per_page=100`])).flat(); }
function validateAsset(asset) {
  if (!/^[A-Za-z0-9_][A-Za-z0-9_.-]*$/.test(asset.name) || basename(asset.name) !== asset.name || asset.size <= 0 || asset.size > (64<<20) ||
      !/^sha256:[0-9a-f]{64}$/.test(asset.digest ?? '')) throw Error('Unsafe asset name or missing digest');
}
function checkBackup(directory, release) {
  for (const asset of release.assets) {
    validateAsset(asset);
    const bytes = readFileSync(join(directory, release.tag_name, asset.name));
    if (bytes.length !== asset.size || `sha256:${createHash('sha256').update(bytes).digest('hex')}` !== asset.digest) throw Error('Retired asset backup mismatch');
  }
}

export function validatePlan(plan, fresh, tag) {
  const now = planPrune(fresh, tag);
  if (plan.skip) return;
  if (now.skip || stamp(now.keep) !== stamp(plan.keep)) throw Error('Kept release changed; no deletion');
  for (const old of plan.targets) {
    const current = now.targets.find(r => r.id === old.id);
    if (!current || stamp(current) !== stamp(old)) throw Error('Retired release changed; no deletion');
    if (!Number.isSafeInteger(old.id) || old.id <= 0) throw Error('Invalid release ID');
  }
}

function main(args) {
  const [mode, repo, tag, directoryArgument] = args;
  if (!['backup','apply'].includes(mode) || !/^[A-Za-z0-9][A-Za-z0-9_.-]*\/[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(repo ?? '') || !directoryArgument) throw Error('usage: prune-rc.mjs backup|apply owner/repo tag directory');
  const directory = resolve(directoryArgument);
  if (mode === 'backup') {
    const plan = {...planPrune(list(repo), tag), repo, tag};
    mkdirSync(directory, {recursive:true, mode:0o700});
    for (const old of plan.targets) {
      for (const asset of old.assets) validateAsset(asset);
      gh(['release','download',old.tag_name,'--repo',repo,'--dir',join(directory,old.tag_name)]);
      checkBackup(directory, old);
    }
    writeFileSync(join(directory,'plan.json'), JSON.stringify(plan,null,2), {mode:0o600});
    process.stdout.write(`${plan.targets.length} older RC backups verified. Git tags will be kept.\n`);
    return;
  }
  const plan = JSON.parse(readFileSync(join(directory,'plan.json'),'utf8'));
  if (plan.repo !== repo || plan.tag !== tag) throw Error('Backup plan targets a different release or repository');
  validatePlan(plan, list(repo), tag);
  if (plan.skip) { process.stdout.write(`${plan.skip}; no releases removed.\n`); return; }
  for (const old of plan.targets) checkBackup(directory, old);
  // The workflow uploads the verified backup before this command is allowed
  // to run. Only release entries/assets are deleted, never git refs/tags.
  for (const old of plan.targets) {
    if (stamp(api(repo,`releases/${old.id}`)) !== stamp(old)) throw Error('Release changed during cleanup');
    gh(['api','--method','DELETE',`repos/${repo}/releases/${old.id}`]);
    process.stdout.write(`Removed ${old.tag_name}; backup and git tag kept.\n`);
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { main(process.argv.slice(2)); } catch (error) { process.stderr.write(`${error.message}\n`); process.exitCode=1; }
}
