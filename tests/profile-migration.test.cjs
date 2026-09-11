const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {PGlite}=require('@electric-sql/pglite');
test('profile migration forward and rollback preserve identities and names',async()=>{
 const db=new PGlite();
 try {
  const initial=fs.readFileSync(__dirname+'/../database/migrations/00001_initial.sql','utf8').split('-- +goose Up')[1].split('-- +goose Down')[0];
  const migration=fs.readFileSync(__dirname+'/../database/migrations/00005_user_profile.sql','utf8');
  await db.exec(initial);
  await db.exec("INSERT INTO users(id,wechat_subject_hash,display_name) VALUES ('alice',decode('01','hex'),'Alice')");
  await db.exec(migration.split('-- +goose Up')[1].split('-- +goose Down')[0]);
  assert.equal((await db.query("SELECT profile_revision FROM users WHERE id='alice'")).rows[0].profile_revision,0);
  await assert.rejects(db.exec("UPDATE users SET avatar_jpeg=decode(repeat('aa',36865),'hex') WHERE id='alice'"));
  await db.exec(migration.split('-- +goose Down')[1]);
  assert.equal((await db.query("SELECT display_name FROM users WHERE id='alice'")).rows[0].display_name,'Alice');
  await db.exec(migration.split('-- +goose Up')[1].split('-- +goose Down')[0]);
 } finally {await db.close();}
});
