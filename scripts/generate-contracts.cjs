// Generates public Go/TS shapes from the canonical schema; unions remain validated RawMessage in Go.
const fs = require('node:fs');
const path = require('node:path');
const cp = require('node:child_process');
const root = path.resolve(__dirname, '..');
const schema = JSON.parse(fs.readFileSync(path.join(root, 'contracts/protocol.schema.json'), 'utf8'));
const defs = schema.$defs;
const name = s => (s[0].toUpperCase() + s.slice(1)).replace(/Id$/, 'ID');
function go(s) {
  if (s.$ref) return name(s.$ref.split('/').pop());
  if (s.anyOf?.some(x => x.type === 'null')) return '*' + go(s.anyOf.find(x => x.type !== 'null'));
  if (s.oneOf || s.anyOf) return 'json.RawMessage';
  if (s.const !== undefined || s.enum || s.type === 'string') return 'string';
  if (s.type === 'boolean') return 'bool';
  if (s.type === 'integer') return 'int64';
  if (s.type === 'number') return 'float64';
  if (s.type === 'array') return '[]' + go(s.items);
  if (s.type === 'object' && s.properties) {
    return 'struct {\n' + Object.entries(s.properties).map(([k, v]) => `\t${name(k)} ${go(v)} \`json:"${k}${s.required?.includes(k) ? '' : ',omitempty'}"\``).join('\n') + '\n}';
  }
  return 'map[string]json.RawMessage';
}
function ts(s) {
  if (s.$ref) return s.$ref.split('/').pop();
  if (s.const !== undefined) return JSON.stringify(s.const);
  if (s.enum) return s.enum.map(x => JSON.stringify(x)).join(' | ');
  if (s.oneOf || s.anyOf) return (s.oneOf || s.anyOf).map(x => '(' + ts(x) + ')').join(' | ');
  if (s.type === 'integer' || s.type === 'number') return 'number';
  if (['string', 'boolean', 'null'].includes(s.type)) return s.type;
  if (s.type === 'array') return '(' + ts(s.items) + ')[]';
  if (s.type === 'object' && s.properties) return '{ ' + Object.entries(s.properties).map(([k, v]) => `${JSON.stringify(k)}${s.required?.includes(k) ? '' : '?'}: ${ts(v)}`).join('; ') + ' }';
  if (s.additionalProperties && typeof s.additionalProperties === 'object') return 'Record<string, ' + ts(s.additionalProperties) + '>';
  return 'Record<string, unknown>';
}
let goText = '// Code generated from contracts/protocol.schema.json. DO NOT EDIT.\npackage protocol\nimport "encoding/json"\n';
let tsText = '// Generated from protocol.schema.json. Structural types; use schema validation for all constraints.\n';
for (const [n, s] of Object.entries(defs)) { goText += `type ${name(n)} ${go(s)}\n`; tsText += `export type ${n} = ${ts(s)};\n`; }
goText = cp.execFileSync('gofmt', { input: goText, encoding: 'utf8' });
for (const [file, text] of [['internal/protocol/types_generated.go', goText], ['contracts/protocol.generated.d.ts', tsText]]) {
  const target = path.join(root, file);
  if (process.argv.includes('--check')) { if (!fs.existsSync(target) || fs.readFileSync(target, 'utf8') !== text) throw Error('Generated contract drift: ' + file); }
  else { fs.mkdirSync(path.dirname(target), { recursive: true }); fs.writeFileSync(target, text); }
}
console.log('Go/TS contract shapes: ' + Object.keys(defs).length + ' definitions');
