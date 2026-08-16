ALTER TABLE outbox ADD COLUMN claimed_at timestamptz;
ALTER TABLE outbox ADD COLUMN claim_expires_at timestamptz;
