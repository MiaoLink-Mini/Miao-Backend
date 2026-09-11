const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { PGlite } = require('@electric-sql/pglite');
const source = fs.readFileSync(path.join(__dirname, '../database/migrations/00001_initial.sql'), 'utf8');
const up = source.split('-- +goose Up')[1].split('-- +goose Down')[0];
const down = source.split('-- +goose Down')[1];
let db;
const scalar = async sql => (await db.query(sql)).rows[0];
const rollback = async fn => {
  await db.exec('BEGIN');
  try { await fn(); } finally { await db.exec('ROLLBACK'); }
};
const seed = `
INSERT INTO users(id,wechat_subject_hash) VALUES ('alice',decode('01','hex')),('bob',decode('02','hex'));
INSERT INTO nodes(id,public_key,name,platform,version) VALUES
 ('n1',decode(repeat('01',32),'hex'),'Alice device','macos','1'),
 ('n2',decode(repeat('02',32),'hex'),'Bob device','linux','1');
INSERT INTO node_access(user_id,node_id) VALUES ('alice','n1'),('bob','n2');
INSERT INTO projects(id,node_id,name) VALUES ('p1','n1','Project 1'),('p2','n2','Project 2');
INSERT INTO agents(id,node_id,name,state,adapter_version,capabilities) VALUES
 ('a1','n1','Agent 1','ready','1','{}'),('a2','n2','Agent 2','ready','1','{}');
INSERT INTO sessions(id,user_id,node_id,project_id,agent_id,title,state,mode,capabilities) VALUES
 ('s1','alice','n1','p1','a1','Session 1','completed','managed','{}'),
 ('s2','bob','n2','p2','a2','Session 2','completed','managed','{}');
INSERT INTO turns(id,session_id,state) VALUES ('t1','s1','completed'),('t2','s2','completed');
UPDATE sessions SET current_turn_id='t1' WHERE id='s1';
UPDATE sessions SET current_turn_id='t2' WHERE id='s2';
INSERT INTO operations(user_id,id,kind,request_hash,state,node_id,session_id,expected_turn_id,deadline_at)
 VALUES ('alice','op1','send',decode(repeat('ab',32),'hex'),'accepted','n1','s1','t1',now()+interval '15 seconds');
INSERT INTO interaction_requests(id,session_id,turn_id,user_id,kind,state,definition,expires_at)
 VALUES ('r1','s1','t1','alice','question','pending','{}',now()+interval '5 minutes');
`;

before(async () => {
  db = new PGlite();
  await db.exec(`BEGIN; ${up} ${seed} COMMIT;`);
});
after(async () => { if (db) await db.close(); });

test('forward migration creates all design tables in isolated PostgreSQL fixture', async () => {
  const expected = [...up.matchAll(/CREATE TABLE (\w+)/g)].map(m => m[1]).sort();
  const actual = (await db.query("SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename")).rows.map(r => r.tablename);
  assert.deepEqual(actual, expected);
  assert.equal(expected.length, 25);
  assert.equal((await scalar("SELECT history_state FROM sessions WHERE id='s1'")).history_state, 'available');
});

test('session cannot combine a foreign Node project or Agent', async () => {
  await assert.rejects(db.query("UPDATE sessions SET project_id='p2' WHERE id='s1'"), e => e.code === '23503');
  await assert.rejects(db.query("UPDATE sessions SET agent_id='a2' WHERE id='s1'"), e => e.code === '23503');
});

test('native-control migration upgrades and rolls back without losing existing journals', async () => {
  const migration = fs.readFileSync(path.join(__dirname, '../database/migrations/00002_native_control.sql'), 'utf8');
  const forward = migration.split('-- +goose Up')[1].split('-- +goose Down')[0], backward = migration.split('-- +goose Down')[1];
  await rollback(async () => {
    await db.exec(forward);
    await db.exec("UPDATE operations SET kind='native' WHERE id='op1'");
    assert.equal((await scalar("SELECT kind FROM operations WHERE id='op1'")).kind, 'native');
  });
  await rollback(async () => { await db.exec(forward); await db.exec(backward); });
  await db.exec(`BEGIN; ${forward} UPDATE operations SET kind='native' WHERE id='op1'; COMMIT;`);
  try {
    await db.exec('BEGIN'); await assert.rejects(db.exec(backward), e => e.code === '23514'); await db.exec('ROLLBACK');
    assert.equal((await scalar("SELECT kind FROM operations WHERE id='op1'")).kind, 'native', 'failed downgrade retains receipt');
  } finally { await db.exec(`BEGIN; UPDATE operations SET kind='send' WHERE id='op1'; ${backward} COMMIT;`); }
});

test('session and operation cannot claim a different user owner', async () => {
  await assert.rejects(db.query("UPDATE sessions SET user_id='bob' WHERE id='s1'"), e => e.code === '23503');
  await assert.rejects(db.query("UPDATE operations SET user_id='bob' WHERE id='op1'"), e => e.code === '23503');
});

test('request and notification cannot reference another user/session turn', async () => {
  await assert.rejects(db.query("UPDATE interaction_requests SET turn_id='t2' WHERE id='r1'"), e => e.code === '23503');
  await assert.rejects(db.query("INSERT INTO notifications(id,user_id,session_id,kind,title,summary) VALUES ('bad','bob','s1','question','x','x')"), e => e.code === '23503');
});

test('idempotency is unique per user while allowing a different user namespace', async () => {
  await assert.rejects(db.query("INSERT INTO operations SELECT * FROM operations WHERE id='op1'"), e => e.code === '23505');
  await rollback(async () => {
    await db.query("INSERT INTO operations(user_id,id,kind,request_hash,state,deadline_at) VALUES ('bob','op1','mark_read',decode(repeat('bb',32),'hex'),'accepted',now()+interval '1 minute')");
    assert.equal((await scalar("SELECT count(*)::int AS n FROM operations WHERE id='op1'")).n, 2);
  });
});

test('outbox cannot silently change the operation target Node', async () => {
  await assert.rejects(db.query("INSERT INTO command_outbox(user_id,operation_id,node_id,node_epoch,state,deadline_at) VALUES ('alice','op1','n2',1,'pending',now()+interval '1 minute')"), e => e.code === '23503');
});

test('only one active turn per session', async () => {
  await rollback(async () => {
    await db.query("INSERT INTO turns(id,session_id,state) VALUES ('active1','s1','running')");
    await assert.rejects(db.query("INSERT INTO turns(id,session_id,state) VALUES ('active2','s1','starting')"), e => e.code === '23505');
  });
});

test('deferred current-turn relationship rejects a different session at commit', async () => {
  await db.exec('BEGIN');
  try {
    await db.query("UPDATE sessions SET current_turn_id='t2' WHERE id='s1'");
    await assert.rejects(db.exec('COMMIT'), e => e.code === '23503');
  } finally { await db.exec('ROLLBACK'); }
  assert.equal((await scalar("SELECT current_turn_id FROM sessions WHERE id='s1'")).current_turn_id, 't1');
});

test('request decision must reserve an operation and respect expiry ordering', async () => {
  await assert.rejects(db.query("UPDATE interaction_requests SET state='resolved' WHERE id='r1'"), e => e.code === '23514');
  await assert.rejects(db.query("UPDATE interaction_requests SET expires_at=created_at WHERE id='r1'"), e => e.code === '23514');
  await rollback(async () => {
    await db.query("UPDATE interaction_requests SET state='deciding',decision='{}',decision_operation_id='op1' WHERE id='r1'");
    assert.equal((await scalar("SELECT state FROM interaction_requests WHERE id='r1'")).state, 'deciding');
  });
});

test('transaction rollback also rolls back sequence allocation, leaving no cursor gap', async () => {
  await rollback(async () => {
    await db.query("UPDATE sessions SET last_sequence=last_sequence+1 WHERE id='s1'");
    await db.query("INSERT INTO session_events(session_id,sequence,id,node_id,turn_id,type,data) VALUES ('s1',1,'rolled_back','n1','t1','message.completed','{}')");
  });
  assert.equal(Number((await scalar("SELECT last_sequence FROM sessions WHERE id='s1'")).last_sequence), 0);
  await rollback(async () => {
    const row = await scalar("UPDATE sessions SET last_sequence=last_sequence+1 WHERE id='s1' RETURNING last_sequence");
    assert.equal(Number(row.last_sequence), 1);
    await db.query("INSERT INTO session_events(session_id,sequence,id,node_id,turn_id,type,data) VALUES ('s1',1,'reused_one','n1','t1','message.completed','{}')");
  });
});

test('event receipt survives event deletion and remains unique', async () => {
  await rollback(async () => {
    await db.query("INSERT INTO session_events(session_id,sequence,id,node_id,turn_id,source_event_id,source_sequence,source_hash,type,data) VALUES ('s1',1,'e1','n1','t1','src1',1,decode(repeat('cc',32),'hex'),'message.completed','{}')");
    await db.query("INSERT INTO node_event_receipts(node_id,source_event_id,session_id,source_sequence,source_hash,gateway_sequence) VALUES ('n1','src1','s1',1,decode(repeat('cc',32),'hex'),1)");
    await db.query("DELETE FROM session_events WHERE id='e1'");
    assert.equal((await scalar("SELECT count(*)::int AS n FROM node_event_receipts WHERE source_event_id='src1'")).n, 1);
    await assert.rejects(db.query("INSERT INTO node_event_receipts SELECT * FROM node_event_receipts WHERE source_event_id='src1'"), e => e.code === '23505');
  });
});

test('sequence bounds and operation terminal error shape are enforced', async () => {
  await assert.rejects(db.query("UPDATE sessions SET last_sequence=9007199254740992 WHERE id='s1'"), e => e.code === '23514');
  await assert.rejects(db.query("UPDATE operations SET state='failed' WHERE id='op1'"), e => e.code === '23514');
});

test('timeline identity includes turn, not just reusable item ID', async () => {
  await rollback(async () => {
    await db.query("INSERT INTO turns(id,session_id,state) VALUES ('t3','s1','completed')");
    await db.query("INSERT INTO timeline_items(session_id,turn_id,item_id,first_sequence,last_sequence,body) VALUES ('s1','t1','same',1,1,'{}'),('s1','t3','same',2,2,'{}')");
    assert.equal((await scalar("SELECT count(*)::int AS n FROM timeline_items WHERE item_id='same'")).n, 2);
  });
});

test('initial Down removes only the design tables, then Up can be reapplied', async () => {
  await db.exec('CREATE TABLE unrelated_fixture (id integer PRIMARY KEY); INSERT INTO unrelated_fixture VALUES (7)');
  await db.exec(`BEGIN; ${down} COMMIT;`);
  assert.deepEqual((await db.query("SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename")).rows.map(r => r.tablename), ['unrelated_fixture']);
  assert.equal((await scalar('SELECT id FROM unrelated_fixture')).id, 7);
  await db.exec(`BEGIN; ${up} ${seed} COMMIT;`);
  assert.equal((await scalar('SELECT count(*)::int AS n FROM sessions')).n, 2);
});
