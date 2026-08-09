ALTER TABLE nodes_dynamic ADD COLUMN udp_failure_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes_dynamic ADD COLUMN udp_circuit_open_since INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes_dynamic ADD COLUMN last_udp_probe_attempt_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes_dynamic ADD COLUMN last_udp_probe_success_ns INTEGER NOT NULL DEFAULT 0;
