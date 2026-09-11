const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const baseline = require('../contracts/source-baseline.json');
const coverage = require('../contracts/frontend-coverage.json').requirements;
const root = path.resolve(process.env.WEAGENT_FRONTEND_ROOT || path.join(__dirname, '../../WeAgent-Frontend'));
const hash = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');

test('historical design inventory still exists; frozen page and requirement identities have not drifted', () => {
  // source-baseline is the original design snapshot, not a freeze on implementation.
  // Live transport/runtime changes are covered by executable frontend and integration tests.
  for (const file of baseline.files) {
    assert.ok(fs.existsSync(path.join(root, file.path)), file.path);
    if (file.path === 'miniprogram/app.json') {
      assert.equal(hash(path.join(__dirname,'../contracts/app.original.json')),file.sha256);
      const current=JSON.parse(fs.readFileSync(path.join(root,file.path)));
      const original=require('../contracts/app.original.json');
      assert.deepEqual(current.pages.slice(0,original.pages.length),original.pages);
      assert.deepEqual(current.pages.slice(original.pages.length),['pages/workspace/index','pages/guide/index','pages/share/index','pages/shared/index','pages/profile/index','pages/about/index']);
      assert.deepEqual(current.tabBar.list,original.tabBar.list);
      assert.equal(current.tabBar.custom,true);
      for(const [key,value] of Object.entries(original.usingComponents))assert.equal(current.usingComponents[key],value);
    }
    if (file.path === 'miniprogram/catalog/requirements.js') {
      // Keep the exact historical bytes as provenance, but allow review metadata to evolve.
      assert.equal(hash(path.join(__dirname, '../contracts/requirements.original.js')), file.sha256, file.path);
      const original = require('../contracts/requirements.original.js');
      const actual = require(path.join(root, file.path));
      const fields = ['id', 'name', 'entry', 'criterion', 'source', 'scope', 'group', 'groupName', 'route'];
      assert.deepEqual(actual.map(row => fields.map(field => row[field])), original.map(row => fields.map(field => row[field])));
    }
  }
});

test('auxiliary design sources match the reviewed baseline', () => {
  for (const file of baseline.references) {
    assert.equal(hash(path.resolve(__dirname, '../contracts/baseline-sources', file.path)), file.sha256, file.path);
  }
});

test('all original pages and the native workspace are explicitly mapped', () => {
  const pages = JSON.parse(fs.readFileSync(path.join(root, 'miniprogram/app.json'), 'utf8')).pages;
  const doc = fs.readFileSync(path.join(__dirname, '../docs/frontend-contract.md'), 'utf8');
  assert.equal(pages.length, 26);
  assert.deepEqual(pages.filter(p=>!['pages/workspace/index','pages/guide/index','pages/share/index','pages/shared/index','pages/profile/index','pages/about/index'].includes(p)), baseline.pages);
  for (const page of pages) assert.ok(doc.includes(`| ${page.split('/')[1]} |`), page);
});

test('all 194 requirements preserve design identities and separate historical from current review status', () => {
  const actual = require(path.join(root, 'miniprogram/catalog/requirements.js'));
  assert.equal(coverage.length, 194);
  assert.equal(new Set(coverage.map(r => r.id)).size, actual.length);
  assert.equal(actual.filter(r => r.legacyStatus === '演示交互').length, 98);
  assert.equal(baseline.frontendDemoCount, 98);
  assert.deepEqual(coverage.map(r => [r.id, r.name, r.frontendStatus, r.route]), actual.map(r => [r.id, r.name, r.legacyStatus, r.route]));
  const currentCounts = actual.reduce((counts, item) => { counts[item.status] = (counts[item.status] || 0) + 1; return counts; }, {});
  assert.deepEqual(currentCounts, { '未逐项验收': 121, '已接入待验收': 64, '部分实现': 9 });
  for (const item of actual) { assert.ok(item.record.mapping); assert.ok(item.record.adapterScope); assert.ok(item.record.validation); }
  // The coverage file below is design responsibility, not a current completion claim.
  for (const item of coverage) {
    assert.equal(item.backendImplemented, false);
    assert.ok(['gateway-v1', 'node-v1', 'client-local', 'deferred'].includes(item.owner));
    assert.ok(item.note.length > 10);
  }
});

test('live adapter consumes canonical schema; slash native entries require explicit capabilities', () => {
  const { LiveGateway } = require(path.join(root, 'miniprogram/services/live-gateway.js'));
  const { suggestions } = require(path.join(root, 'miniprogram/utils/slash-commands.js'));
  const { validate } = require(path.join(root, 'miniprogram/services/wire.js'));
  assert.equal(typeof LiveGateway.prototype.native, 'function');
  assert.equal(require(path.join(root, 'miniprogram/config.js')).mode, 'live');
  assert.equal(suggestions('/model', { capabilities: {} }).items.length, 0);
  assert.equal(suggestions('/model', { capabilities: { models: true } }).command.panel, 'config');
  assert.equal(suggestions('/compact', { capabilities: { compact: true } }).command.panel, 'compact');
  assert.throws(() => validate('NativeControl', { expectedTurnId: 't', capabilityRevision: 1, control: { action: 'shell', command: 'arbitrary' } }));
  assert.equal(coverage.find(r => r.id === 'N03').owner, 'deferred');
  assert.equal(coverage.find(r => r.id === 'G05').owner, 'node-v1');
  assert.equal(coverage.find(r => r.id === 'J11').owner, 'gateway-v1');
});
