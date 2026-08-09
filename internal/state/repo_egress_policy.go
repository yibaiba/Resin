package state

import (
	"database/sql"
	"errors"

	"github.com/Resinat/Resin/internal/model"
)

func (r *StateRepo) UpsertEgressIPPolicy(policy model.EgressIPPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.db.Exec(`
		INSERT INTO egress_ip_policies (
			egress_ip, status, max_active_leases, resume_at_ns, updated_at_ns
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(egress_ip) DO UPDATE SET
			status = excluded.status,
			max_active_leases = excluded.max_active_leases,
			resume_at_ns = excluded.resume_at_ns,
			updated_at_ns = excluded.updated_at_ns
	`, policy.EgressIP, policy.Status, policy.MaxActiveLeases, policy.ResumeAtNs, policy.UpdatedAtNs)
	return err
}

func (r *StateRepo) GetEgressIPPolicy(egressIP string) (*model.EgressIPPolicy, error) {
	row := r.db.QueryRow(`
		SELECT egress_ip, status, max_active_leases, resume_at_ns, updated_at_ns
		FROM egress_ip_policies WHERE egress_ip = ?
	`, egressIP)
	var policy model.EgressIPPolicy
	if err := row.Scan(
		&policy.EgressIP,
		&policy.Status,
		&policy.MaxActiveLeases,
		&policy.ResumeAtNs,
		&policy.UpdatedAtNs,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &policy, nil
}

func (r *StateRepo) ListEgressIPPolicies() ([]model.EgressIPPolicy, error) {
	rows, err := r.db.Query(`
		SELECT egress_ip, status, max_active_leases, resume_at_ns, updated_at_ns
		FROM egress_ip_policies ORDER BY egress_ip
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var policies []model.EgressIPPolicy
	for rows.Next() {
		var policy model.EgressIPPolicy
		if err := rows.Scan(
			&policy.EgressIP,
			&policy.Status,
			&policy.MaxActiveLeases,
			&policy.ResumeAtNs,
			&policy.UpdatedAtNs,
		); err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}
