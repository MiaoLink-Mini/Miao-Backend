-- +goose Up
ALTER TABLE sessions ADD COLUMN pinned boolean NOT NULL DEFAULT false;
ALTER TABLE sessions ADD COLUMN tags jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(tags)='array' AND jsonb_array_length(tags)<=10);
ALTER TABLE sessions ADD COLUMN archived boolean NOT NULL DEFAULT false;
ALTER TABLE sessions ADD COLUMN parent_session_id text;
ALTER TABLE sessions ADD COLUMN origin_point text;
ALTER TABLE sessions ADD COLUMN origin_mode text NOT NULL DEFAULT 'new' CHECK(origin_mode IN ('new','resume','fork','clone','attach','import'));
ALTER TABLE sessions ADD CONSTRAINT sessions_parent_owner FOREIGN KEY(parent_session_id,user_id) REFERENCES sessions(id,user_id) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX sessions_parent ON sessions(parent_session_id);
ALTER TABLE operations DROP CONSTRAINT operations_kind_check;
ALTER TABLE operations ADD CONSTRAINT operations_kind_check CHECK(kind IN ('create','send','queue','cancel','respond','pair','revoke','mark_read','native','organize','delete_history','import_history','subscribe'));
ALTER TABLE audit_entries DROP CONSTRAINT audit_entries_action_check;
ALTER TABLE audit_entries ADD CONSTRAINT audit_entries_action_check CHECK(action IN ('login','pair','revoke','create','send','queue','cancel','respond','mark_read','native','organize','delete_history','import_history','subscribe'));
CREATE TABLE subscription_identities (user_id text PRIMARY KEY REFERENCES users(id), encrypted_subject bytea NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE subscription_consents (user_id text NOT NULL REFERENCES users(id), template_id text NOT NULL, credits integer NOT NULL DEFAULT 0 CHECK(credits BETWEEN 0 AND 100), decision text NOT NULL CHECK(decision IN ('accept','reject','ban','filter')), updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(user_id,template_id));
ALTER TABLE notifications ADD CONSTRAINT notifications_owner_unique UNIQUE(id,user_id);
CREATE TABLE subscription_outbox (id text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id), notification_id text NOT NULL, template_id text NOT NULL, payload jsonb NOT NULL, state text NOT NULL CHECK(state IN ('pending','sending','sent','failed','unknown')), error_code integer, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(notification_id,template_id), FOREIGN KEY(notification_id,user_id) REFERENCES notifications(id,user_id) ON DELETE CASCADE);
CREATE INDEX subscription_pending ON subscription_outbox(created_at) WHERE state='pending';
-- +goose Down
-- Deliberately refuse a downgrade while receipts reference the new operation kinds.
ALTER TABLE operations DROP CONSTRAINT operations_kind_check;
ALTER TABLE operations ADD CONSTRAINT operations_kind_check CHECK(kind IN ('create','send','queue','cancel','respond','pair','revoke','mark_read','native'));
ALTER TABLE audit_entries DROP CONSTRAINT audit_entries_action_check;
ALTER TABLE audit_entries ADD CONSTRAINT audit_entries_action_check CHECK(action IN ('login','pair','revoke','create','send','queue','cancel','respond','mark_read','native'));
DROP TABLE subscription_outbox;
ALTER TABLE notifications DROP CONSTRAINT notifications_owner_unique;
DROP TABLE subscription_consents;
DROP TABLE subscription_identities;
ALTER TABLE sessions DROP CONSTRAINT sessions_parent_owner;
DROP INDEX sessions_parent;
ALTER TABLE sessions DROP COLUMN pinned, DROP COLUMN tags, DROP COLUMN archived, DROP COLUMN parent_session_id, DROP COLUMN origin_mode, DROP COLUMN origin_point;
