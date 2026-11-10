import { spawnSync } from 'node:child_process';
import { existsSync, lstatSync, readFileSync, writeFileSync } from 'node:fs';
import * as prettier from 'prettier';

const mode = process.argv[2];
if (!['--write', '--check'].includes(mode) || process.argv.length !== 3) {
  console.error('Usage: node scripts/format.mjs --write|--check');
  process.exit(1);
}

function run(command, args, input) {
  const result = spawnSync(command, args, { input, encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(result.stderr || `${command} exited with ${result.status}`);
  return result.stdout;
}

const paths = run('git', ['ls-files', '--cached', '--others', '--exclude-standard', '-z']);
let checked = 0;
let changed = 0;
let failed = false;

for (const filepath of new Set(paths.split('\0').filter(Boolean))) {
  if (!existsSync(filepath) || !lstatSync(filepath).isFile()) continue;
  try {
    const go = filepath.endsWith('.go');
    const info = await prettier.getFileInfo(filepath, { ignorePath: ['.gitignore', '.prettierignore'] });
    if (info.ignored || (!go && !info.inferredParser)) continue;
    const source = readFileSync(filepath, 'utf8');
    const formatted = go
      ? run('gofmt', [], source)
      : await prettier.format(source, { ...(await prettier.resolveConfig(filepath)), filepath });
    checked++;
    if (source === formatted) continue;
    changed++;
    if (mode === '--write') {
      writeFileSync(filepath, formatted);
      console.log(`Formatted ${filepath}`);
    } else {
      console.error(`Needs formatting: ${filepath}`);
    }
  } catch (error) {
    failed = true;
    console.error(`${filepath}: ${error.message}`);
  }
}

if (failed || (changed > 0 && mode !== '--write')) {
  console.error('Run make format.');
  process.exitCode = 1;
} else {
  console.log(`Formatting passed (${checked} files${mode === '--write' ? `, ${changed} updated` : ''}).`);
}
