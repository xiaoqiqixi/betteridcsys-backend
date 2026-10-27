CREATE INDEX ix_audit_date_id ON audit_events(created_at,id);
CREATE INDEX ix_audit_actor_date_id ON audit_events(actor_email,created_at,id);
CREATE INDEX ix_audit_action_date_id ON audit_events(action,created_at,id);
