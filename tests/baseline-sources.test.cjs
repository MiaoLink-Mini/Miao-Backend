'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const baseline = require('../contracts/source-baseline.json');
const { verifyBaselineSources } = require('../scripts/check-baseline-sources.cjs');
const source = path.resolve(__dirname, '../contracts/baseline-sources');

function fixture(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'golink-baseline-test-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  for (const file of baseline.references) fs.copyFileSync(path.join(source, file.path), path.join(directory, file.path));
  return directory;
}

test('the three recovered originals retain the previously recorded hashes', () => {
  assert.deepEqual(verifyBaselineSources(source, baseline.references), baseline.references);
  assert.equal(baseline.references.length, 3);
});

test('a missing original fails instead of falling back to another same-name document', t => {
  const directory = fixture(t);
  fs.unlinkSync(path.join(directory, baseline.references[0].path));
  assert.throws(() => verifyBaselineSources(directory, baseline.references), { code: 'ENOENT' });
});

test('an altered original fails without refreshing the historical hash', t => {
  const directory = fixture(t);
  const file = path.join(directory, baseline.references[0].path);
  fs.appendFileSync(file, '\nmodified\n');
  assert.throws(() => verifyBaselineSources(directory, baseline.references), /SHA-256 mismatch/);
  assert.match(fs.readFileSync(file, 'utf8'), /modified/);
});

test('historical original bytes are protected from Git line-ending conversion', () => {
  const attributes = fs.readFileSync(path.join(source, '.gitattributes'), 'utf8');
  assert.equal(attributes, '*.md -text\n');
});
