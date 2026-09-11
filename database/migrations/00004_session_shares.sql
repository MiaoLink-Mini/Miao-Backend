-- +goose Up
CREATE TABLE session_shares (
  id text PRIMARY KEY,
  session_id text NOT NULL,
  owner_id text NOT NULL,
  recipient_id text REFERENCES users(id),
  permission text NOT NULL CHECK (permission IN ('read','control')),
  token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash)=32),
  encrypted_invite bytea,
  create_operation_id text NOT NULL,
  request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  accepted_at timestamptz,
  revoked_at timestamptz,
  FOREIGN KEY(session_id,owner_id) REFERENCES sessions(id,user_id) ON DELETE CASCADE,
  UNIQUE(owner_id,create_operation_id),
  UNIQUE(id,owner_id,recipient_id),
  CHECK (recipient_id IS NULL OR recipient_id<>owner_id),
  CHECK ((recipient_id IS NULL)=(accepted_at IS NULL)),
  CHECK (expires_at>=created_at+interval '15 minutes' AND expires_at<=created_at+interval '7 days')
);
CREATE INDEX session_shares_session ON session_shares(session_id,created_at DESC,id DESC);
CREATE INDEX session_shares_recipient ON session_shares(recipient_id,created_at DESC,id DESC);
CREATE TABLE session_share_commands (
  share_id text NOT NULL REFERENCES session_shares(id) ON DELETE CASCADE,
  actor_id text NOT NULL REFERENCES users(id),
  client_operation_id text NOT NULL,
  owner_id text NOT NULL,
  owner_operation_id text NOT NULL,
  request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
  action text NOT NULL CHECK(action IN ('send','cancel','respond')),
  created_at timestamptz NOT NULL,
  PRIMARY KEY(actor_id,client_operation_id),
  FOREIGN KEY(owner_id,owner_operation_id) REFERENCES operations(user_id,id),
  FOREIGN KEY(share_id,owner_id,actor_id) REFERENCES session_shares(id,owner_id,recipient_id)
);
CREATE INDEX session_share_commands_share ON session_share_commands(share_id,created_at DESC,client_operation_id DESC);
-- +goose Down
DROP TABLE session_share_commands;
DROP TABLE session_shares;
