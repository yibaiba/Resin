ALTER TABLE leases DROP COLUMN rotation_deferred_reason;
ALTER TABLE leases DROP COLUMN rotation_deferred;
ALTER TABLE leases DROP COLUMN rotation_requested_strategy;
ALTER TABLE leases DROP COLUMN rotation_requested_at_ns;
ALTER TABLE leases DROP COLUMN last_rotated_at_ns;
