-- +goose Up
ALTER TABLE operations DROP CONSTRAINT operations_kind_check;
ALTER TABLE operations ADD CONSTRAINT operations_kind_check
  CHECK (kind IN ('create','send','queue','cancel','respond','pair','revoke','mark_read','native'));
ALTER TABLE audit_entries DROP CONSTRAINT audit_entries_action_check;
ALTER TABLE audit_entries ADD CONSTRAINT audit_entries_action_check
  CHECK (action IN ('login','pair','revoke','create','send','queue','cancel','respond','mark_read','native'));

-- +goose Down
-- Refuse rollback if native receipts exist; never delete journals to make a downgrade fit.
ALTER TABLE audit_entries DROP CONSTRAINT audit_entries_action_check;
ALTER TABLE audit_entries ADD CONSTRAINT audit_entries_action_check
  CHECK (action IN ('login','pair','revoke','create','send','queue','cancel','respond','mark_read'));
ALTER TABLE operations DROP CONSTRAINT operations_kind_check;
ALTER TABLE operations ADD CONSTRAINT operations_kind_check
  CHECK (kind IN ('create','send','queue','cancel','respond','pair','revoke','mark_read'));
