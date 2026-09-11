const {test,before,after}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');const path=require('node:path');
const {PGlite}=require('@electric-sql/pglite');
const root=path.join(__dirname,'..');
const migrations=['00001_initial.sql','00002_native_control.sql','00003_workspace.sql'];
const initial='00001_initial.sql';
const readMigration=name=>fs.readFileSync(path.join(root,'database/migrations',name),'utf8');
const up=s=>s.split('-- +goose Up')[1].split('-- +goose Down')[0];
const subscriptions=fs.readFileSync(path.join(root,'internal/gateway/subscriptions.go'),'utf8');
const sql=part=>{const found=[...subscriptions.matchAll(/`([^`]+)`/g)].map(m=>m[1]).filter(s=>s.includes(part));assert.equal(found.length,1,'exact SQL occurrence: '+part);return found[0];};
const deliver=sql('SELECT o.id,o.user_id,o.template_id,o.payload,i.encrypted_subject');
let db;
const seed=`
INSERT INTO users(id,wechat_subject_hash) VALUES ('alice',decode('01','hex')),('bob',decode('02','hex'));
INSERT INTO nodes(id,public_key,name,platform,version) VALUES ('n1',decode(repeat('01',32),'hex'),'Alice device','macos','1'),('n2',decode(repeat('02',32),'hex'),'Bob device','linux','1');
INSERT INTO node_access(user_id,node_id) VALUES ('alice','n1'),('bob','n2');
INSERT INTO projects(id,node_id,name) VALUES ('p1','n1','Project 1'),('p2','n2','Project 2');
INSERT INTO agents(id,node_id,name,state,adapter_version,capabilities) VALUES ('a1','n1','Agent 1','ready','1','{}'),('a2','n2','Agent 2','ready','1','{}');
INSERT INTO sessions(id,user_id,node_id,project_id,agent_id,title,state,mode,capabilities) VALUES ('s1','alice','n1','p1','a1','Session 1','completed','managed','{}'),('s2','bob','n2','p2','a2','Session 2','completed','managed','{}');
INSERT INTO turns(id,session_id,state) VALUES ('t1','s1','completed'),('t2','s2','completed');
UPDATE sessions SET current_turn_id='t1' WHERE id='s1'; UPDATE sessions SET current_turn_id='t2' WHERE id='s2';
INSERT INTO operations(user_id,id,kind,request_hash,state,node_id,session_id,expected_turn_id,deadline_at) VALUES ('alice','op1','send',decode(repeat('ab',32),'hex'),'accepted','n1','s1','t1',now()+interval '15 seconds');
INSERT INTO notifications(id,user_id,session_id,kind,title,summary) VALUES ('notice','alice','s1','question','Question','Summary');
INSERT INTO subscription_identities(user_id,encrypted_subject) VALUES ('alice',decode('010203','hex'));
INSERT INTO subscription_consents(user_id,template_id,credits,decision) VALUES ('alice','configured-template',1,'accept');
INSERT INTO subscription_outbox(id,user_id,notification_id,template_id,payload,state) VALUES ('push','alice','notice','configured-template','{}','pending');`;
before(async()=>{db=new PGlite();await db.exec('BEGIN;'+[initial,...migrations.slice(1)].map(n=>up(readMigration(n))).join('\n')+seed+'COMMIT;');});
after(async()=>{await db?.close();});
const rollback=async fn=>{await db.exec('BEGIN');try{return await fn();}finally{await db.exec('ROLLBACK');}};

test('workspace migration adds organization, provenance and encrypted delivery storage',async()=>{
 const columns=(await db.query("SELECT column_name FROM information_schema.columns WHERE table_name='sessions'")).rows.map(x=>x.column_name);
 for(const name of ['pinned','tags','archived','parent_session_id','origin_mode'])assert.ok(columns.includes(name));
 const row=(await db.query("SELECT pinned,tags,archived,origin_mode FROM sessions WHERE id='s1'")).rows[0];assert.deepEqual(row,{pinned:false,tags:[],archived:false,origin_mode:'new'});
});
test('organization never changes the active execution state or current turn',async()=>rollback(async()=>{
 await db.query("UPDATE sessions SET pinned=true,tags='[\"review\"]',archived=true,title='Organized' WHERE id='s1'");
 assert.deepEqual((await db.query("SELECT state,current_turn_id FROM sessions WHERE id='s1'")).rows[0],{state:'completed',current_turn_id:'t1'});
}));
test('history provenance cannot cross user ownership at commit',async()=>{
 await db.exec('BEGIN');await db.query("UPDATE sessions SET parent_session_id='s2' WHERE id='s1'");await assert.rejects(db.exec('COMMIT'),e=>e.code==='23503');await db.exec('ROLLBACK');
});
test('outbox receiver cannot be substituted for a different notification owner',async()=>rollback(async()=>{
 await assert.rejects(db.query("UPDATE subscription_outbox SET user_id='bob' WHERE id='push'"),e=>e.code==='23503');
}));
test('notification and template pair reserves at most one delivery',async()=>rollback(async()=>{
 await assert.rejects(db.query("INSERT INTO subscription_outbox SELECT 'another',user_id,notification_id,template_id,payload,state,error_code,created_at,updated_at FROM subscription_outbox WHERE id='push'"),e=>e.code==='23505');
}));
test('the actual credit reservation SQL consumes once and never becomes negative',async()=>rollback(async()=>{
 const q=sql('UPDATE subscription_consents SET credits=credits-1');
 assert.equal((await db.query(q,['alice','configured-template'])).affectedRows,1);assert.equal((await db.query(q,['alice','configured-template'])).affectedRows,0);
 assert.equal((await db.query("SELECT credits FROM subscription_consents WHERE user_id='alice'")).rows[0].credits,0);
}));
test('the actual delivery selector returns only an authorized pending item',async()=>rollback(async()=>{
 assert.equal((await db.query(deliver)).rows.length,1);await db.query("UPDATE subscription_outbox SET state='unknown'");assert.equal((await db.query(deliver)).rows.length,0);
}));
test('an ambiguous in-flight send is not retried after process restart',async()=>rollback(async()=>{
 await db.query("UPDATE subscription_outbox SET state='sending'");await db.query(sql("UPDATE subscription_outbox SET state='unknown'"));
 assert.equal((await db.query("SELECT state FROM subscription_outbox")).rows[0].state,'unknown');assert.equal((await db.query(deliver)).rows.length,0);
}));
test('revoking device access removes pending items from the delivery selector',async()=>rollback(async()=>{
 await db.query("UPDATE node_access SET revoked_at=now() WHERE user_id='alice'");assert.equal((await db.query(deliver)).rows.length,0);
}));
test('rejected consent does not disable in-app notifications and cannot send a pending push',async()=>rollback(async()=>{
 await db.query("UPDATE subscription_consents SET decision='reject',credits=0 WHERE user_id='alice'");assert.equal((await db.query(deliver)).rows.length,0);assert.equal((await db.query('SELECT id FROM notifications')).rows.length,1);
}));
test('purged history and deleted notifications cannot be delivered',async()=>rollback(async()=>{
 await db.query("UPDATE sessions SET history_state='purged' WHERE id='s1'");assert.equal((await db.query(deliver)).rows.length,0);
 await db.query("DELETE FROM notifications WHERE id='notice'");assert.equal((await db.query('SELECT id FROM subscription_outbox')).rows.length,0);
}));
test('downgrade refuses to discard newly accepted operation kinds',async()=>{
 await db.exec("BEGIN; UPDATE operations SET kind='subscribe' WHERE id='op1'; COMMIT");await db.exec('BEGIN');
 await assert.rejects(db.exec(readMigration('00003_workspace.sql').split('-- +goose Down')[1]),e=>e.code==='23514');await db.exec('ROLLBACK');
 assert.equal((await db.query("SELECT kind FROM operations WHERE id='op1'")).rows[0].kind,'subscribe');
});
