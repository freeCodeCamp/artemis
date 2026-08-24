ALTER TABLE sites ADD COLUMN IF NOT EXISTS state TEXT NOT NULL DEFAULT 'active';
ALTER TABLE sites ADD COLUMN IF NOT EXISTS reserved_at TIMESTAMPTZ;
ALTER TABLE sites ADD COLUMN IF NOT EXISTS reserved_until TIMESTAMPTZ;
ALTER TABLE sites ADD COLUMN IF NOT EXISTS reserved_by TEXT NOT NULL DEFAULT '';
ALTER TABLE sites ADD COLUMN IF NOT EXISTS prev_production TEXT NOT NULL DEFAULT '';
ALTER TABLE sites ADD COLUMN IF NOT EXISTS prev_preview TEXT NOT NULL DEFAULT '';

ALTER TABLE sites ADD CONSTRAINT sites_state_known CHECK (state IN ('active', 'reserved'));
ALTER TABLE sites ADD CONSTRAINT sites_reserved_has_deadline CHECK (state <> 'reserved' OR reserved_until IS NOT NULL);

CREATE INDEX IF NOT EXISTS sites_reserved_until_idx ON sites (reserved_until) WHERE state = 'reserved';
