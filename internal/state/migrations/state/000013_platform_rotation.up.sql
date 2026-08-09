ALTER TABLE platforms ADD COLUMN rotation_interval_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE platforms ADD COLUMN rotation_strategy TEXT NOT NULL DEFAULT 'ANY';
