-- Design baseline. Go application generates opaque IDs; no UUID extension required.
-- +goose Up
CREATE TABLE users (
  id text PRIMARY KEY,
  wechat_subject_hash bytea NOT NULL UNIQUE,
  display_name text NOT NULL DEFAULT '',
  last_revision bigint NOT NULL DEFAULT 0 CHECK (last_revision BETWEEN 0 AND 9007199254740991),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_tokens (
  token_hash bytea PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id),
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_tokens_owner ON user_tokens(user_id);

CREATE TABLE nodes (
  id text PRIMARY KEY,
  public_key bytea NOT NULL UNIQUE CHECK (octet_length(public_key) = 32),
  name text NOT NULL,
  platform text NOT NULL CHECK (platform IN ('windows','macos','linux','other')),
  version text NOT NULL,
  connection_epoch bigint NOT NULL DEFAULT 0 CHECK (connection_epoch BETWEEN 0 AND 9007199254740991),
  credential_version bigint NOT NULL DEFAULT 1,
  revision bigint NOT NULL DEFAULT 1,
  last_seen_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
-- online is derived from a live, epoch-matching socket and heartbeat; never stored here.
CREATE TABLE node_access (
  user_id text NOT NULL REFERENCES users(id),
  node_id text NOT NULL REFERENCES nodes(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz,
  PRIMARY KEY(user_id, node_id),
  UNIQUE(node_id)
);
-- V1: a Node incarnation has one lifetime owner; re-pair after revoke uses a new key/id.
CREATE TABLE node_tokens (
  token_hash bytea PRIMARY KEY,
  node_id text NOT NULL REFERENCES nodes(id),
  credential_version bigint NOT NULL,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz
);
CREATE INDEX node_tokens_owner ON node_tokens(node_id);

CREATE TABLE node_enrollments (
  id text PRIMARY KEY,
  public_key bytea NOT NULL CHECK (octet_length(public_key) = 32),
  code_hash bytea NOT NULL UNIQUE,
  poll_token_hash bytea NOT NULL UNIQUE,
  name text NOT NULL,
  platform text NOT NULL CHECK (platform IN ('windows','macos','linux','other')),
  version text NOT NULL,
  state text NOT NULL CHECK (state IN ('pending','confirmed','expired')),
  node_id text REFERENCES nodes(id),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((state = 'confirmed') = (node_id IS NOT NULL))
);
CREATE TABLE pairing_tickets (
  id text PRIMARY KEY,
  enrollment_id text NOT NULL REFERENCES node_enrollments(id),
  user_id text NOT NULL REFERENCES users(id),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz
);
CREATE TABLE node_challenges (
  id text PRIMARY KEY,
  node_id text NOT NULL REFERENCES nodes(id),
  nonce_hash bytea NOT NULL,
  signing_input text NOT NULL,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz
);

CREATE TABLE projects (
  id text PRIMARY KEY,
  node_id text NOT NULL REFERENCES nodes(id),
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  branch text,
  valid boolean NOT NULL DEFAULT true,
  revision bigint NOT NULL DEFAULT 1,
  UNIQUE(id, node_id)
);
CREATE INDEX projects_node ON projects(node_id);
CREATE TABLE agents (
  id text PRIMARY KEY,
  node_id text NOT NULL REFERENCES nodes(id),
  name text NOT NULL,
  state text NOT NULL CHECK (state IN ('ready','unavailable','error')),
  version text,
  adapter_version text NOT NULL,
  capabilities jsonb NOT NULL CHECK (jsonb_typeof(capabilities) = 'object'),
  capability_revision bigint NOT NULL DEFAULT 1,
  revision bigint NOT NULL DEFAULT 1,
  UNIQUE(id, node_id)
);
CREATE INDEX agents_node ON agents(node_id);

CREATE TABLE sessions (
  id text PRIMARY KEY,
  user_id text NOT NULL,
  node_id text NOT NULL,
  project_id text NOT NULL,
  agent_id text NOT NULL,
  title text NOT NULL,
  state text NOT NULL CHECK (state IN ('idle','running','waiting_approval','waiting_input','cancelling','completed','cancelled','failed','closed')),
  current_turn_id text,
  mode text NOT NULL CHECK (mode IN ('managed','readonly')),
  history_state text NOT NULL DEFAULT 'available' CHECK (history_state IN ('available','purged')),
  capabilities jsonb NOT NULL CHECK (jsonb_typeof(capabilities) = 'object'),
  capability_revision bigint NOT NULL DEFAULT 1,
  revision bigint NOT NULL DEFAULT 1,
  last_sequence bigint NOT NULL DEFAULT 0 CHECK (last_sequence BETWEEN 0 AND 9007199254740991),
  last_source_sequence bigint NOT NULL DEFAULT 0 CHECK (last_source_sequence BETWEEN 0 AND 9007199254740991),
  earliest_sequence bigint NOT NULL DEFAULT 1 CHECK (earliest_sequence > 0 AND earliest_sequence <= last_sequence + 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(user_id, node_id) REFERENCES node_access(user_id, node_id),
  FOREIGN KEY(project_id, node_id) REFERENCES projects(id, node_id),
  FOREIGN KEY(agent_id, node_id) REFERENCES agents(id, node_id),
  UNIQUE(id, user_id),
  UNIQUE(id, node_id)
);
CREATE INDEX sessions_owner_recent ON sessions(user_id, updated_at DESC, id DESC);
CREATE INDEX sessions_node_state ON sessions(node_id, state);
CREATE TABLE turns (
  id text PRIMARY KEY,
  session_id text NOT NULL REFERENCES sessions(id),
  state text NOT NULL CHECK (state IN ('starting','running','waiting_approval','waiting_input','cancelling','completed','cancelled','failed')),
  started_at timestamptz,
  ended_at timestamptz,
  UNIQUE(id, session_id)
);
ALTER TABLE sessions ADD CONSTRAINT sessions_current_turn_fk
  FOREIGN KEY(current_turn_id, id) REFERENCES turns(id, session_id) DEFERRABLE INITIALLY DEFERRED;
CREATE UNIQUE INDEX turns_one_active ON turns(session_id)
  WHERE state IN ('starting','running','waiting_approval','waiting_input','cancelling');

CREATE TABLE operations (
  user_id text NOT NULL REFERENCES users(id),
  id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('create','send','queue','cancel','respond','pair','revoke','mark_read')),
  request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
  state text NOT NULL CHECK (state IN ('accepted','delivered','confirmed','failed','reconciling')),
  node_id text REFERENCES nodes(id),
  session_id text,
  expected_turn_id text,
  node_epoch bigint,
  payload jsonb,
  result jsonb,
  error jsonb,
  revision bigint NOT NULL DEFAULT 1,
  deadline_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id, id),
  UNIQUE(user_id, id, node_id),
  FOREIGN KEY(session_id, user_id) REFERENCES sessions(id, user_id),
  FOREIGN KEY(session_id, node_id) REFERENCES sessions(id, node_id),
  FOREIGN KEY(expected_turn_id, session_id) REFERENCES turns(id, session_id),
  CHECK (expected_turn_id IS NULL OR session_id IS NOT NULL),
  CHECK ((state = 'failed') = (error IS NOT NULL))
);
CREATE INDEX operations_reconcile ON operations(node_id, updated_at) WHERE state IN ('accepted','delivered','reconciling');
CREATE TABLE command_outbox (
  user_id text NOT NULL,
  operation_id text NOT NULL,
  node_id text NOT NULL REFERENCES nodes(id),
  node_epoch bigint NOT NULL,
  state text NOT NULL CHECK (state IN ('pending','dispatching','delivered','uncertain','not_sent')),
  deadline_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id, operation_id),
  FOREIGN KEY(user_id, operation_id, node_id) REFERENCES operations(user_id, id, node_id)
);
CREATE INDEX command_outbox_pending ON command_outbox(deadline_at) WHERE state = 'pending';
CREATE TABLE queue_items (
  id text PRIMARY KEY,
  user_id text NOT NULL,
  session_id text NOT NULL,
  operation_id text NOT NULL,
  next_turn_id text NOT NULL,
  position bigint NOT NULL CHECK (position > 0),
  text_content text NOT NULL,
  state text NOT NULL CHECK (state IN ('queued','starting','consumed','cancelled')),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(session_id, user_id) REFERENCES sessions(id, user_id),
  FOREIGN KEY(user_id, operation_id) REFERENCES operations(user_id, id),
  UNIQUE(session_id, position),
  UNIQUE(session_id, operation_id)
);
-- Queued future turns are not active turns yet; next_turn_id is reserved, not an FK.
CREATE TABLE interaction_requests (
  id text PRIMARY KEY,
  session_id text NOT NULL,
  turn_id text NOT NULL,
  user_id text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('approval','question')),
  state text NOT NULL CHECK (state IN ('pending','deciding','resolved','expired','cancelled')),
  definition jsonb NOT NULL CHECK (jsonb_typeof(definition) = 'object'),
  decision jsonb,
  decision_operation_id text,
  revision bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  resolved_at timestamptz,
  FOREIGN KEY(session_id, user_id) REFERENCES sessions(id, user_id),
  FOREIGN KEY(turn_id, session_id) REFERENCES turns(id, session_id),
  FOREIGN KEY(user_id, decision_operation_id) REFERENCES operations(user_id, id),
  CHECK ((decision IS NULL) = (decision_operation_id IS NULL)),
  CHECK (state NOT IN ('deciding','resolved') OR decision IS NOT NULL),
  CHECK (expires_at > created_at)
);
CREATE INDEX requests_inbox ON interaction_requests(user_id, created_at DESC, id DESC) WHERE state IN ('pending','deciding');
CREATE INDEX requests_expiry ON interaction_requests(expires_at) WHERE state IN ('pending','deciding');

CREATE TABLE session_events (
  session_id text NOT NULL,
  sequence bigint NOT NULL CHECK (sequence BETWEEN 1 AND 9007199254740991),
  id text NOT NULL UNIQUE,
  node_id text NOT NULL,
  turn_id text,
  source_event_id text,
  source_sequence bigint CHECK (source_sequence BETWEEN 1 AND 9007199254740991),
  source_hash bytea,
  type text NOT NULL,
  data jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(session_id, sequence),
  FOREIGN KEY(session_id, node_id) REFERENCES sessions(id, node_id),
  FOREIGN KEY(turn_id, session_id) REFERENCES turns(id, session_id),
  UNIQUE(node_id, source_event_id),
  UNIQUE(session_id, source_sequence),
  CHECK ((source_event_id IS NULL) = (source_sequence IS NULL)),
  CHECK ((source_event_id IS NULL) = (source_hash IS NULL)),
  CHECK (source_hash IS NULL OR octet_length(source_hash) = 32)
);
-- Nullable source fields denote Gateway-generated events. Node dedupe uses non-null source IDs.
CREATE TABLE node_event_receipts (
  node_id text NOT NULL,
  source_event_id text NOT NULL,
  session_id text NOT NULL,
  source_sequence bigint NOT NULL CHECK (source_sequence BETWEEN 1 AND 9007199254740991),
  source_hash bytea NOT NULL CHECK (octet_length(source_hash) = 32),
  gateway_sequence bigint NOT NULL CHECK (gateway_sequence BETWEEN 1 AND 9007199254740991),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(node_id, source_event_id),
  UNIQUE(session_id, source_sequence),
  FOREIGN KEY(session_id, node_id) REFERENCES sessions(id, node_id)
);
-- Independent receipt ledger survives event-prefix compaction for safe source deduplication.
CREATE TABLE timeline_items (
  session_id text NOT NULL REFERENCES sessions(id),
  turn_id text NOT NULL,
  item_id text NOT NULL,
  first_sequence bigint NOT NULL CHECK (first_sequence > 0),
  last_sequence bigint NOT NULL CHECK (last_sequence >= first_sequence),
  body jsonb NOT NULL,
  PRIMARY KEY(session_id, turn_id, item_id),
  FOREIGN KEY(turn_id, session_id) REFERENCES turns(id, session_id)
);
CREATE INDEX timeline_order ON timeline_items(session_id, first_sequence, item_id);
CREATE TABLE checkpoints (
  id text PRIMARY KEY,
  session_id text NOT NULL REFERENCES sessions(id),
  sequence bigint NOT NULL CHECK (sequence BETWEEN 0 AND 9007199254740991),
  item_count bigint NOT NULL CHECK (item_count >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE TABLE checkpoint_items (
  checkpoint_id text NOT NULL REFERENCES checkpoints(id),
  ordinal bigint NOT NULL CHECK (ordinal > 0),
  body jsonb NOT NULL,
  PRIMARY KEY(checkpoint_id, ordinal)
);
CREATE TABLE diff_files (
  id text PRIMARY KEY,
  session_id text NOT NULL REFERENCES sessions(id),
  turn_id text NOT NULL,
  item_id text NOT NULL,
  display_path text NOT NULL,
  language text,
  patch text NOT NULL,
  truncated boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(turn_id, session_id) REFERENCES turns(id, session_id)
);
CREATE TABLE notifications (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id),
  session_id text,
  kind text NOT NULL CHECK (kind IN ('approval','question','completed','failed','system')),
  title text NOT NULL,
  summary text NOT NULL,
  read_at timestamptz,
  revision bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(session_id, user_id) REFERENCES sessions(id, user_id)
);
CREATE INDEX notifications_inbox ON notifications(user_id, created_at DESC, id DESC) WHERE read_at IS NULL;
CREATE TABLE user_changes (
  user_id text NOT NULL REFERENCES users(id),
  revision bigint NOT NULL CHECK (revision BETWEEN 1 AND 9007199254740991),
  resource_type text NOT NULL CHECK (resource_type IN ('node','project','agent','session','request','notification','audit','operation')),
  resource_id text NOT NULL,
  action text NOT NULL CHECK (action IN ('upsert','remove')),
  resource_revision bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(user_id, revision)
);
CREATE TABLE audit_entries (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id),
  operation_id text,
  node_id text,
  session_id text,
  action text NOT NULL CHECK (action IN ('login','pair','revoke','create','send','queue','cancel','respond','mark_read')),
  state text NOT NULL CHECK (state IN ('accepted','delivered','confirmed','failed','reconciling')),
  title text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_owner_recent ON audit_entries(user_id, created_at DESC, id DESC);
-- Audit IDs intentionally are not content FKs: bounded metadata survives content cleanup.

-- +goose Down
DROP TABLE audit_entries;
DROP TABLE user_changes;
DROP TABLE notifications;
DROP TABLE diff_files;
DROP TABLE checkpoint_items;
DROP TABLE checkpoints;
DROP TABLE timeline_items;
DROP TABLE node_event_receipts;
DROP TABLE session_events;
DROP TABLE interaction_requests;
DROP TABLE queue_items;
DROP TABLE command_outbox;
DROP TABLE operations;
ALTER TABLE sessions DROP CONSTRAINT sessions_current_turn_fk;
DROP TABLE turns;
DROP TABLE sessions;
DROP TABLE agents;
DROP TABLE projects;
DROP TABLE node_challenges;
DROP TABLE pairing_tickets;
DROP TABLE node_enrollments;
DROP TABLE node_tokens;
DROP TABLE node_access;
DROP TABLE nodes;
DROP TABLE user_tokens;
DROP TABLE users;
