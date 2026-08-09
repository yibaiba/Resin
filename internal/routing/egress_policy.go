package routing

import (
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Resinat/Resin/internal/model"
)

type EgressIPStatus string

const (
	EgressIPActive   EgressIPStatus = "ACTIVE"
	EgressIPDraining EgressIPStatus = "DRAINING"
	EgressIPBlocked  EgressIPStatus = "BLOCKED"
)

func (s EgressIPStatus) IsValid() bool {
	return s == EgressIPActive || s == EgressIPDraining || s == EgressIPBlocked
}

type EgressIPPolicy struct {
	IP              netip.Addr
	Status          EgressIPStatus
	MaxActiveLeases int64
	ResumeAt        time.Time
}

func (p EgressIPPolicy) EffectiveStatus(now time.Time) EgressIPStatus {
	if p.Status != EgressIPActive && !p.ResumeAt.IsZero() && !now.Before(p.ResumeAt) {
		return EgressIPActive
	}
	return p.Status
}

type egressPolicySnapshot struct {
	items      map[netip.Addr]EgressIPPolicy
	restricted bool
}

// EgressPolicyStore publishes whole immutable policy snapshots to routing.
type EgressPolicyStore struct {
	snapshot atomic.Pointer[egressPolicySnapshot]
	mu       sync.Mutex
}

func NewEgressPolicyStore() *EgressPolicyStore {
	store := &EgressPolicyStore{}
	store.snapshot.Store(&egressPolicySnapshot{items: map[netip.Addr]EgressIPPolicy{}})
	return store
}

func (s *EgressPolicyStore) Replace(models []model.EgressIPPolicy) error {
	items := make(map[netip.Addr]EgressIPPolicy, len(models))
	for _, item := range models {
		policy, err := parseEgressPolicy(item)
		if err != nil {
			return err
		}
		items[policy.IP] = policy
	}
	s.mu.Lock()
	s.publish(items)
	s.mu.Unlock()
	return nil
}

func (s *EgressPolicyStore) Set(item model.EgressIPPolicy) error {
	policy, err := parseEgressPolicy(item)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.snapshot.Load()
	items := make(map[netip.Addr]EgressIPPolicy, len(current.items)+1)
	for ip, existing := range current.items {
		items[ip] = existing
	}
	items[policy.IP] = policy
	s.publish(items)
	return nil
}

func ValidateEgressPolicy(item model.EgressIPPolicy) error {
	_, err := parseEgressPolicy(item)
	return err
}

func (s *EgressPolicyStore) Get(ip netip.Addr) EgressIPPolicy {
	if s == nil || !ip.IsValid() {
		return defaultEgressPolicy(ip)
	}
	return policyFromSnapshot(s.snapshot.Load(), ip)
}

func policyFromSnapshot(snapshot *egressPolicySnapshot, ip netip.Addr) EgressIPPolicy {
	if policy, ok := snapshot.items[ip.Unmap()]; ok {
		return policy
	}
	return defaultEgressPolicy(ip)
}

func (s *EgressPolicyStore) HasRestrictions() bool {
	return s != nil && s.snapshot.Load().restricted
}

func (s *EgressPolicyStore) publish(items map[netip.Addr]EgressIPPolicy) {
	restricted := false
	for _, policy := range items {
		if policy.Status != EgressIPActive || policy.MaxActiveLeases > 0 {
			restricted = true
			break
		}
	}
	s.snapshot.Store(&egressPolicySnapshot{items: items, restricted: restricted})
}

func parseEgressPolicy(item model.EgressIPPolicy) (EgressIPPolicy, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(item.EgressIP))
	if err != nil {
		return EgressIPPolicy{}, err
	}
	status := EgressIPStatus(strings.ToUpper(strings.TrimSpace(item.Status)))
	if !status.IsValid() {
		return EgressIPPolicy{}, &invalidEgressPolicyError{field: "status", value: item.Status}
	}
	if item.MaxActiveLeases < 0 {
		return EgressIPPolicy{}, &invalidEgressPolicyError{field: "max_active_leases", value: item.MaxActiveLeases}
	}
	resumeAt := time.Time{}
	if item.ResumeAtNs > 0 {
		resumeAt = time.Unix(0, item.ResumeAtNs)
	}
	return EgressIPPolicy{
		IP: ip.Unmap(), Status: status, MaxActiveLeases: item.MaxActiveLeases,
		ResumeAt: resumeAt,
	}, nil
}

type invalidEgressPolicyError struct {
	field string
	value any
}

func (e *invalidEgressPolicyError) Error() string {
	return "invalid egress policy " + e.field
}

func defaultEgressPolicy(ip netip.Addr) EgressIPPolicy {
	return EgressIPPolicy{IP: ip.Unmap(), Status: EgressIPActive}
}
