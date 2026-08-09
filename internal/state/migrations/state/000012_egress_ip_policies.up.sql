CREATE TABLE egress_ip_policies (
    egress_ip TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('ACTIVE', 'DRAINING', 'BLOCKED')),
    max_active_leases INTEGER NOT NULL DEFAULT 0 CHECK (max_active_leases >= 0),
    resume_at_ns INTEGER NOT NULL DEFAULT 0,
    updated_at_ns INTEGER NOT NULL
);
