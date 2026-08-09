package state

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Resinat/Resin/internal/model"
)

// ReplaceRoutingRules atomically replaces the ordered routing-rule set.
func (r *StateRepo) ReplaceRoutingRules(rules []model.RoutingRule) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM routing_rules"); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO routing_rules (
		id, name, enabled, priority, host_type, host_value, ports_json, network,
		action, platform_id, override_explicit_platform, created_at_ns, updated_at_ns
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, rule := range rules {
		if err := insertRoutingRule(stmt, rule); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertRoutingRule(stmt *sql.Stmt, rule model.RoutingRule) error {
	portsJSON, err := json.Marshal(rule.Ports)
	if err != nil {
		return fmt.Errorf("encode routing rule %s ports: %w", rule.ID, err)
	}
	var platformID any
	if rule.PlatformID != "" {
		platformID = rule.PlatformID
	}
	_, err = stmt.Exec(
		rule.ID, rule.Name, rule.Enabled, rule.Priority, rule.HostType, rule.HostValue,
		string(portsJSON), rule.Network, rule.Action, platformID,
		rule.OverrideExplicitPlatform, rule.CreatedAtNs, rule.UpdatedAtNs,
	)
	return err
}

// ListRoutingRules returns all rules in effective order.
func (r *StateRepo) ListRoutingRules() ([]model.RoutingRule, error) {
	rows, err := r.db.Query(`SELECT id, name, enabled, priority, host_type, host_value,
		ports_json, network, action, platform_id, override_explicit_platform,
		created_at_ns, updated_at_ns FROM routing_rules ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := make([]model.RoutingRule, 0)
	for rows.Next() {
		rule, err := scanRoutingRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

type routingRuleScanner interface {
	Scan(dest ...any) error
}

func scanRoutingRule(scanner routingRuleScanner) (model.RoutingRule, error) {
	var rule model.RoutingRule
	var portsJSON string
	var platformID sql.NullString
	if err := scanner.Scan(
		&rule.ID, &rule.Name, &rule.Enabled, &rule.Priority, &rule.HostType,
		&rule.HostValue, &portsJSON, &rule.Network, &rule.Action, &platformID,
		&rule.OverrideExplicitPlatform, &rule.CreatedAtNs, &rule.UpdatedAtNs,
	); err != nil {
		return model.RoutingRule{}, err
	}
	if err := json.Unmarshal([]byte(portsJSON), &rule.Ports); err != nil {
		return model.RoutingRule{}, fmt.Errorf("decode routing rule %s ports: %w", rule.ID, err)
	}
	if rule.Ports == nil {
		rule.Ports = []model.RoutingPortRange{}
	}
	rule.PlatformID = platformID.String
	return rule, nil
}

// RoutingRuleReferencesPlatform reports whether a rule targets the platform.
func (r *StateRepo) RoutingRuleReferencesPlatform(platformID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM routing_rules WHERE platform_id = ?
	)`, platformID).Scan(&exists)
	return exists, err
}
