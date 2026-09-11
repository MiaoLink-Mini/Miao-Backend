const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const Ajv = require('ajv/dist/2020');
const addFormats = require('ajv-formats');
const SwaggerParser = require('@apidevtools/swagger-parser');
const schema = require('../contracts/protocol.schema.json');
const api = require('../contracts/http.openapi.json');
const examples = require('../contracts/examples.json').cases;
const ajv = new Ajv({ strict: true, allErrors: true, validateFormats: true });
addFormats(ajv);
ajv.addSchema(schema);

test('every canonical definition compiles in JSON Schema 2020-12 strict mode', () => {
  assert.ok(Object.keys(schema.$defs).length >= 70);
  for (const name of Object.keys(schema.$defs)) {
    assert.equal(typeof ajv.getSchema(`${schema.$id}#/$defs/${name}`), 'function', name);
  }
});

for (const fixture of examples) {
  test(`schema ${fixture.valid ? 'accepts' : 'rejects'}: ${fixture.name}`, () => {
    const validate = ajv.getSchema(`${schema.$id}#/$defs/${fixture.schema}`);
    assert.ok(validate, fixture.schema);
    const actual = validate(fixture.value);
    assert.equal(actual, fixture.valid, JSON.stringify(validate.errors));
  });
}

test('OpenAPI 3.1.1 validates with local references only', async () => {
  const parsed = await SwaggerParser.validate(path.join(__dirname, '../contracts/http.openapi.json'), {
    resolve: { http: false }
  });
  assert.equal(parsed.openapi, '3.1.1');
});

test('API operations use unique IDs, canonical models, scoped auth and mutation keys', () => {
  const ids = new Set();
  let count = 0;
  for (const [route, methods] of Object.entries(api.paths)) {
    for (const [method, operation] of Object.entries(methods)) {
      count++;
      assert.ok(!ids.has(operation.operationId), operation.operationId);
      ids.add(operation.operationId);
      assert.ok(operation.responses.default, route);
      for (const param of route.matchAll(/\{([^}]+)\}/g)) {
        assert.ok(operation.parameters.some(p => p.in === 'path' && p.name === param[1] && p.required));
      }
      for (const response of Object.values(operation.responses)) {
        assert.match(response.content['application/json'].schema.$ref, /^\.\/protocol\.schema\.json#\/\$defs\//);
      }
      if (operation.requestBody) {
        assert.match(operation.requestBody.content['application/json'].schema.$ref, /^\.\/protocol\.schema\.json#\/\$defs\//);
      }
      if (route.startsWith('/v1/') && !route.startsWith('/v1/node/') && !route.startsWith('/v1/auth/') && route !== '/v1/pairings/preview' && method === 'post' && !['redeemShare','revokeShare'].includes(operation.operationId)) {
        assert.ok(operation.parameters.some(p => p.$ref === '#/components/parameters/IdempotencyKey'), route);
      }
      if (operation.security?.length === 0) {
        assert.ok(['/healthz', '/readyz', '/v1/auth/wechat', '/v1/node/enrollments', '/v1/node/auth/challenges', '/v1/node/auth/prove'].includes(route), route);
      }
    }
  }
  assert.equal(count, 59);
  for (const name of ['getUserProfile','updateUserProfile']) assert.ok(ids.has(name));
  // Redeem is first-recipient compare-and-set; revoke sets an existing timestamp only.
  for(const name of ['createShare','sharedCommand','redeemShare','revokeShare'])assert.ok(ids.has(name));
  for(const operation of ['organizeSession','deleteHistory','exportHistory','importHistory','getSubscriptionConfig','recordSubscriptionConsent']) assert.ok(ids.has(operation));
  assert.equal(api['x-websockets'].length, 2);
  assert.deepEqual(api.security, [{ UserBearer: [] }]);
});

test('every local JSON reference resolves and no external native protocol leaks into paths', () => {
  const walk = value => {
    if (!value || typeof value !== 'object') return;
    if (value.$ref) {
      const ref = value.$ref;
      const doc = ref.startsWith('./protocol.schema.json#') ? schema : ref.startsWith('#/components/') ? api : schema;
      const fragment = ref.slice(ref.indexOf('#') + 2);
      let target = doc;
      for (const part of fragment.split('/')) target = target?.[part.replace(/~1/g, '/').replace(/~0/g, '~')];
      assert.ok(target, ref);
    }
    for (const child of Object.values(value)) if (typeof child === 'object') walk(child);
  };
  walk(schema);
  walk(api);
  assert.ok(!Object.keys(api.paths).some(p => /shell|exec|pty|setOnline|emit|native-command/.test(p)));
});

test('current frontend requirement: question and approval share one typed lifecycle', () => {
  const types = schema.$defs.InteractionRequest.oneOf;
  assert.deepEqual(types.map(t => t.properties.kind.const), ['approval', 'question']);
  for (const type of types) {
    assert.deepEqual(type.properties.state.enum, ['pending', 'deciding', 'resolved', 'expired', 'cancelled']);
  }
  assert.ok(schema.$defs.SessionState.enum.includes('cancelling'));
  assert.ok(schema.$defs.SessionState.enum.includes('closed'));
  assert.ok(!schema.$defs.SessionState.enum.includes('error'));
});
