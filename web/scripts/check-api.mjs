// Fails when src/lib/api/schema.d.ts is not what `npm run gen:api` produces
// from ../api/openapi.yaml (the spec changed without regenerating).
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const committed = 'src/lib/api/schema.d.ts';
const dir = mkdtempSync(join(tmpdir(), 'acp-api-'));
const out = join(dir, 'schema.d.ts');
try {
  execFileSync('npx', ['openapi-typescript', '../api/openapi.yaml', '-o', out], {
    stdio: ['ignore', 'ignore', 'inherit'],
  });
  if (readFileSync(out, 'utf8') !== readFileSync(committed, 'utf8')) {
    console.error(`${committed} is out of date: run "npm run gen:api" in web/ and commit it.`);
    process.exit(1);
  }
  console.log(`${committed} is up to date.`);
} finally {
  rmSync(dir, { recursive: true, force: true });
}
