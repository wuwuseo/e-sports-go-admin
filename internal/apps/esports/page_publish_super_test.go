package esports

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"go-admin/internal/ent/esportspagedocument"
	"go-admin/internal/ent/esportspageversion"
	"go-admin/internal/global"
)

func TestPagePublishSuperAdminCanReviewOwnRequest(t *testing.T) {
	db := openPageDB(t, "esports_page_super_self_review")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	admin := seedAdminAccount(t, db, "page-super-review")
	channel := pageChannelCustomerHome
	seedPageDocument(t, db, channel, pageTestComponents())
	version, err := publishPage(t.Context(), db, channel, admin.ID, pagePublishInput{Mode: "immediate"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	route := "/admin/pages/:channel/publish-review/:versionId/approve"
	path := fmt.Sprintf("/admin/pages/%s/publish-review/%d/approve", channel, version.ID)
	status, payload := postJSONToHandler(t, admin, false, approvePagePublishHandler, route, path,
		`{"isSuperAdmin":true}`)
	if status != http.StatusForbidden {
		t.Fatalf("非超管自审应被拒，即使请求体伪造超管字段：%d %+v", status, payload)
	}
	status, payload = postJSONToHandler(t, admin, true, approvePagePublishHandler, route, path, `{}`)
	if status != http.StatusOK {
		t.Fatalf("超级管理员应可通过自己提交的发布申请：%d %+v", status, payload)
	}
	version, err = db.EsportsPageVersion.Get(t.Context(), version.ID)
	if err != nil || version.Status != esportspageversion.StatusPublished {
		t.Fatalf("超级管理员自审后版本应生效：%+v err=%v", version, err)
	}
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(t.Context())
	if err != nil || doc.PublishedVersionNo != version.VersionNo {
		t.Fatalf("文档应指向新发布版本：%+v err=%v", doc, err)
	}
}
