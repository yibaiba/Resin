ALTER TABLE leases ADD COLUMN selected_platform_id TEXT NOT NULL DEFAULT '';

UPDATE leases SET selected_platform_id = platform_id WHERE selected_platform_id = '';
