package global

import (
	"sync"
	"time"

	"github.com/casbin/casbin/v3"
)

const casbinPolicyRefreshInterval = time.Second

var casbinPolicyState = struct {
	sync.RWMutex
	lastReload     map[string]time.Time
	reloadRequired map[string]bool
}{
	lastReload:     make(map[string]time.Time),
	reloadRequired: make(map[string]bool),
}

// WithCasbinRead serializes Enforce/model reads against policy and role-link writes.
// ponytail: one process-wide lock is sufficient for the current single Enforcer manager;
// split it per domain only if lock contention becomes measurable.
func WithCasbinRead(fn func()) {
	casbinPolicyState.RLock()
	defer casbinPolicyState.RUnlock()
	fn()
}

// WithCasbinWrite serializes every mutation of the shared non-synchronized Enforcer.
func WithCasbinWrite(fn func()) {
	casbinPolicyState.Lock()
	defer casbinPolicyState.Unlock()
	fn()
}

// ReloadCasbinPolicy reloads the current process and keeps it fail-closed after errors.
func ReloadCasbinPolicy(domain string, enforcer *casbin.Enforcer) error {
	casbinPolicyState.Lock()
	defer casbinPolicyState.Unlock()
	casbinPolicyState.reloadRequired[domain] = true
	if err := enforcer.LoadPolicy(); err != nil {
		return err
	}
	casbinPolicyState.lastReload[domain] = time.Now()
	casbinPolicyState.reloadRequired[domain] = false
	return nil
}

// MutateCasbinPolicy reloads before a Casbin management API write so the
// adapter and in-memory model are changed from the same current state.
func MutateCasbinPolicy(domain string, enforcer *casbin.Enforcer, mutate func() error) error {
	casbinPolicyState.Lock()
	defer casbinPolicyState.Unlock()
	casbinPolicyState.reloadRequired[domain] = true
	if err := enforcer.LoadPolicy(); err != nil {
		return err
	}
	if err := mutate(); err != nil {
		return err
	}
	casbinPolicyState.lastReload[domain] = time.Now()
	casbinPolicyState.reloadRequired[domain] = false
	return nil
}

// RefreshCasbinPolicyIfStale bounds stale policies in prefork/multi-instance deployments.
// A failed reload remains required, so authorization requests fail closed until recovery.
func RefreshCasbinPolicyIfStale(domain string, enforcer *casbin.Enforcer) error {
	casbinPolicyState.Lock()
	defer casbinPolicyState.Unlock()
	if !casbinPolicyState.reloadRequired[domain] && time.Since(casbinPolicyState.lastReload[domain]) < casbinPolicyRefreshInterval {
		return nil
	}
	if err := enforcer.LoadPolicy(); err != nil {
		casbinPolicyState.reloadRequired[domain] = true
		return err
	}
	casbinPolicyState.lastReload[domain] = time.Now()
	casbinPolicyState.reloadRequired[domain] = false
	return nil
}
