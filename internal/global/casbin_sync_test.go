package global

import (
	"errors"
	"testing"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
)

const casbinSyncTestModel = `
[request_definition]
r = sub, dom, obj, act
[policy_definition]
p = sub, dom, obj, act
[role_definition]
g = _, _, _
[policy_effect]
e = some(where (p.eft == allow))
[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && r.obj == p.obj && r.act == p.act
`

type failingLoadAdapter struct {
	fail  bool
	loads int
}

func (a *failingLoadAdapter) LoadPolicy(model.Model) error {
	a.loads++
	if a.fail {
		return errors.New("load failed")
	}
	return nil
}

func (*failingLoadAdapter) SavePolicy(model.Model) error                { return nil }
func (*failingLoadAdapter) AddPolicy(string, string, []string) error    { return nil }
func (*failingLoadAdapter) RemovePolicy(string, string, []string) error { return nil }
func (*failingLoadAdapter) RemoveFilteredPolicy(string, string, int, ...string) error {
	return nil
}

func TestRefreshCasbinPolicyIfStale_RetriesAfterLoadFailure(t *testing.T) {
	m, err := model.NewModelFromString(casbinSyncTestModel)
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	adapter := &failingLoadAdapter{}
	enforcer, err := casbin.NewEnforcer(m, adapter)
	if err != nil {
		t.Fatalf("create enforcer: %v", err)
	}
	const domain = "refresh-failure-test"
	casbinPolicyState.Lock()
	delete(casbinPolicyState.lastReload, domain)
	casbinPolicyState.reloadRequired[domain] = true
	casbinPolicyState.Unlock()
	t.Cleanup(func() {
		casbinPolicyState.Lock()
		delete(casbinPolicyState.lastReload, domain)
		delete(casbinPolicyState.reloadRequired, domain)
		casbinPolicyState.Unlock()
	})

	adapter.loads = 0
	adapter.fail = true
	if err := RefreshCasbinPolicyIfStale(domain, enforcer); err == nil {
		t.Fatal("failed policy reload should fail authorization refresh")
	}
	adapter.fail = false
	if err := RefreshCasbinPolicyIfStale(domain, enforcer); err != nil {
		t.Fatalf("retry policy reload: %v", err)
	}
	if adapter.loads != 2 {
		t.Fatalf("failed reload was not retried: loads=%d", adapter.loads)
	}
}
