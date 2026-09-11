'use strict';
const fs = require('node:fs');
const path = require('node:path');
const { createHash } = require('node:crypto');

function verifyBaselineSources(sourceDirectory, references) {
  return references.map(file => {
    if (typeof file.path !== 'string' || path.basename(file.path) !== file.path ||
        file.path.includes('\\') || file.path === '.' || file.path === '..') {
      throw new Error('Historical source must be an explicit file basename');
    }
    const target = path.join(sourceDirectory, file.path);
    const actual = createHash('sha256').update(fs.readFileSync(target)).digest('hex');
    if (actual !== file.sha256) {
      throw new Error(`Historical source SHA-256 mismatch: ${file.path}`);
    }
    return { path: file.path, sha256: actual };
  });
}

module.exports = { verifyBaselineSources };

if (require.main === module) {
  try {
    const baseline = require('../contracts/source-baseline.json');
    const verified = verifyBaselineSources(
      path.resolve(__dirname, '../contracts/baseline-sources'), baseline.references);
    for (const file of verified) console.log(`Verified ${file.path}: ${file.sha256}`);
    if (process.env.GITHUB_STEP_SUMMARY) {
      const lines = ['### Historical source inputs', '',
        ...verified.map(file => `- ${file.path}: verified against the unchanged source baseline`), ''];
      fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY, lines.join('\n'));
    }
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
