package adapter

import (
	"testing"

	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	_ "github.com/mattn/go-sqlite3"
)

const metadataPolicyModel = `
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

func TestPolicyMutationMatchesModelFieldsAndPreservesMetadata(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:adapter-metadata?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { _ = client.Close() })
	adp, err := NewAdapterWithClient(client)
	if err != nil {
		t.Fatalf("create adapter: %v", err)
	}
	m, err := model.NewModelFromString(metadataPolicyModel)
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	enforcer, err := casbin.NewEnforcer(m, adp)
	if err != nil {
		t.Fatalf("create enforcer: %v", err)
	}

	oldRule := []string{"editor", "default", "/old", "GET"}
	if added, addErr := enforcer.AddPolicy(stringInterfaces(oldRule)...); addErr != nil || !added {
		t.Fatalf("add policy: added=%v err=%v", added, addErr)
	}
	if err := adp.RemovePolicy("p", "p", nil); err != nil {
		t.Fatalf("empty remove policy: %v", err)
	}
	if count := client.CasbinRule.Query().Where(casbinrule.PtypeEQ("p")).CountX(t.Context()); count != 1 {
		t.Fatalf("empty rule removed persisted policies: %d", count)
	}
	stored := client.CasbinRule.Query().Where(
		casbinrule.PtypeEQ("p"), casbinrule.V0EQ("editor"), casbinrule.V2EQ("/old"),
	).OnlyX(t.Context())
	client.CasbinRule.UpdateOne(stored).SetV4("api").SetV5("metadata").SaveX(t.Context())
	if err := enforcer.LoadPolicy(); err != nil {
		t.Fatalf("reload policy: %v", err)
	}

	newRule := []string{"editor", "default", "/new", "POST"}
	if updated, updateErr := enforcer.UpdatePolicy(oldRule, newRule); updateErr != nil || !updated {
		t.Fatalf("update policy: updated=%v err=%v", updated, updateErr)
	}
	updated := client.CasbinRule.Query().Where(
		casbinrule.PtypeEQ("p"), casbinrule.V0EQ("editor"), casbinrule.V2EQ("/new"),
	).OnlyX(t.Context())
	if updated.V4 != "api" || updated.V5 != "metadata" {
		t.Fatalf("metadata lost after update: v4=%q v5=%q", updated.V4, updated.V5)
	}

	if removed, removeErr := enforcer.RemovePolicy(stringInterfaces(newRule)...); removeErr != nil || !removed {
		t.Fatalf("remove policy: removed=%v err=%v", removed, removeErr)
	}
	if count := client.CasbinRule.Query().Where(casbinrule.PtypeEQ("p"), casbinrule.V0EQ("editor")).CountX(t.Context()); count != 0 {
		t.Fatalf("metadata policy remained in storage: %d", count)
	}
}

func stringInterfaces(values []string) []interface{} {
	result := make([]interface{}, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}
