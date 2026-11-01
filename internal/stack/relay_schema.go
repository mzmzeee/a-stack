package stack

const migration16 = `
ALTER TABLE agents ADD COLUMN pane_not_before INTEGER NOT NULL DEFAULT 0;
UPDATE agents SET pane_not_before=COALESCE((SELECT max(0,next_attempt) FROM notice_targets d WHERE d.recipient_id=agents.id AND d.reason IN ('notice_waiting_mail','mail_checked')),0);
ALTER TABLE mail_mentions ADD COLUMN extra_consumed INTEGER NOT NULL DEFAULT 0 CHECK(extra_consumed IN (0,1));
UPDATE mail_mentions SET extra_consumed=1 WHERE notice_state='submitted' OR (SELECT count(*) FROM notice_batch_refs r JOIN notice_batches b ON b.id=r.batch_id WHERE r.message_id=mail_mentions.message_id AND r.recipient_id=mail_mentions.recipient_id AND b.state IN ('submitted','uncertain','submitting'))>=2;
ALTER TABLE notice_batches ADD COLUMN delivery_owner TEXT NOT NULL DEFAULT 'local' CHECK(delivery_owner IN ('local','relay'));
CREATE TABLE notice_relay_leases(
 session_id TEXT NOT NULL REFERENCES sessions(id), recipient_id TEXT NOT NULL REFERENCES agents(id),
 notice_id TEXT NOT NULL UNIQUE, binding_revision INTEGER NOT NULL, binding_hash TEXT NOT NULL,
 lease_hash TEXT NOT NULL, expires_at INTEGER NOT NULL, instance_id TEXT NOT NULL,
 PRIMARY KEY(session_id,recipient_id)
) WITHOUT ROWID;
CREATE INDEX notice_relay_expiry ON notice_relay_leases(expires_at);
DROP INDEX agents_runtime;
CREATE UNIQUE INDEX agents_runtime ON agents(COALESCE(json_extract(binding,'$.relay_machine'),''),runtime_id) WHERE runtime_id IS NOT NULL;
`
