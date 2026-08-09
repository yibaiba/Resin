ALTER TABLE leases ADD COLUMN last_rotated_at_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE leases ADD COLUMN rotation_requested_at_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE leases ADD COLUMN rotation_requested_strategy TEXT NOT NULL DEFAULT '';
ALTER TABLE leases ADD COLUMN rotation_deferred INTEGER NOT NULL DEFAULT 0;
ALTER TABLE leases ADD COLUMN rotation_deferred_reason TEXT NOT NULL DEFAULT '';
