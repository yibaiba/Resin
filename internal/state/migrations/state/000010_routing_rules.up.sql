CREATE TABLE routing_rules (
	id                         TEXT PRIMARY KEY,
	name                       TEXT NOT NULL,
	enabled                    INTEGER NOT NULL DEFAULT 1,
	priority                   INTEGER NOT NULL,
	host_type                  TEXT NOT NULL,
	host_value                 TEXT NOT NULL DEFAULT '',
	ports_json                 TEXT NOT NULL DEFAULT '[]',
	network                    TEXT NOT NULL DEFAULT 'ANY',
	action                     TEXT NOT NULL,
	platform_id                TEXT REFERENCES platforms(id) ON DELETE RESTRICT,
	override_explicit_platform INTEGER NOT NULL DEFAULT 0,
	created_at_ns              INTEGER NOT NULL,
	updated_at_ns              INTEGER NOT NULL
);

CREATE INDEX routing_rules_priority_idx ON routing_rules(priority, id);
CREATE INDEX routing_rules_platform_idx ON routing_rules(platform_id);
