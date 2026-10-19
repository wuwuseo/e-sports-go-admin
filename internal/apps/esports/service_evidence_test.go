package esports

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/migrate"
)

// seedServiceEvidenceFixture 构造一名已开工的陪玩 + 一张服务中的订单，供凭证提交用例使用。
func seedServiceEvidenceFixture(t *testing.T, name string) (*ent.Client, *ent.EsportsPlayer, *ent.EsportsOrder, time.Time) {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	ctx := t.Context()
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	account := db.User.Create().SetEmail(name + "@example.com").SetUID(name + "-uid").SetStatus("active").SaveX(ctx)
	player := seedPlayer(t, db, account.ID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, name+"-order")
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	return db, player, order, now
}

// seedEvidenceAttachment 构造一条附件记录，供凭证校验用例使用。
func seedEvidenceAttachment(t *testing.T, db *ent.Client, uploadedBy, directory, mime string) *ent.Attachment {
	t.Helper()
	return db.Attachment.Create().SetName("服务截图").SetOriginalName("proof.png").
		SetPath(directory + "/proof.png").SetMimeType(mime).SetExtension("png").
		SetDirectory(directory).SetSize(128).SetUploadedBy(uploadedBy).SaveX(t.Context())
}

// 陪玩提交本人服务段凭证成功：附件 ID 以 JSON 数组落进 evidence_ref，结单口径仍识别为有凭证。
func TestServiceEvidenceSubmitOwnSegment(t *testing.T) {
	db, player, order, now := seedServiceEvidenceFixture(t, "esports_service_evidence_ok")
	defer db.Close()
	ctx := t.Context()
	account := db.User.GetX(ctx, player.UserID)
	evidence := seedEvidenceAttachment(t, db, "user:"+account.UID, serviceEvidenceDirectory, "image/png")

	finished, err := finishPlayerServiceWithEvidence(ctx, db, player.ID, order.ID, 1, 60, []int{evidence.ID}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if finished.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("订单未转待结单：%+v", finished)
	}
	segment := db.EsportsServiceSegment.Query().OnlyX(ctx)
	if segment.Status != "finished" {
		t.Fatalf("服务段未结束：%+v", segment)
	}
	var ids []int
	if err := json.Unmarshal([]byte(segment.EvidenceRef), &ids); err != nil || len(ids) != 1 || ids[0] != evidence.ID {
		t.Fatalf("凭证引用 JSON 不正确：%q, %v", segment.EvidenceRef, err)
	}
	// 结单提交口径（§3.4）：服务段有凭证即视为已提交。
	if !segmentSubmitted(segment) {
		t.Fatalf("segmentSubmitted 未识别凭证：%+v", segment)
	}
	// 结单条件口径：evidence_count 按图片附件张数计（本用例单段一个附件 = 1 张）。
	facts, err := closeConditionFactsOf(ctx, db, order)
	if err != nil {
		t.Fatal(err)
	}
	if facts.EvidenceCount != 1 {
		t.Fatalf("结单口径证据数不正确：%d", facts.EvidenceCount)
	}
}

// 非本人服务段提交凭证被拒（403）：本人无服务中服务段，而订单服务段属于他人。
func TestServiceEvidenceNonOwnerForbidden(t *testing.T) {
	db, _, order, now := seedServiceEvidenceFixture(t, "esports_service_evidence_owner")
	defer db.Close()
	ctx := t.Context()
	otherAccount := db.User.Create().SetEmail("other-evidence@example.com").SetUID("other-evidence-uid").SetStatus("active").SaveX(ctx)
	other := seedPlayer(t, db, otherAccount.ID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	if _, err := finishPlayerServiceWithEvidence(ctx, db, other.ID, order.ID, 1, 60, nil, now.Add(time.Minute)); !errors.Is(err, errForbidden) {
		t.Fatalf("非本人提交凭证未按 403 拒绝：%v", err)
	}
}

// 附件校验：非本人上传 / 非图片 / 非私有目录均被拒。
func TestServiceEvidenceRejectsInvalidAttachment(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, db *ent.Client, account, other *ent.User) *ent.Attachment
	}{
		{
			name: "非本人上传",
			build: func(t *testing.T, db *ent.Client, account, other *ent.User) *ent.Attachment {
				return seedEvidenceAttachment(t, db, "user:"+other.UID, serviceEvidenceDirectory, "image/png")
			},
		},
		{
			name: "非图片",
			build: func(t *testing.T, db *ent.Client, account, other *ent.User) *ent.Attachment {
				return seedEvidenceAttachment(t, db, "user:"+account.UID, serviceEvidenceDirectory, "application/pdf")
			},
		},
		{
			name: "非私有目录",
			build: func(t *testing.T, db *ent.Client, account, other *ent.User) *ent.Attachment {
				return seedEvidenceAttachment(t, db, "user:"+account.UID, "public/screenshots", "image/png")
			},
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, player, order, now := seedServiceEvidenceFixture(t, "esports_evidence_invalid_"+string(rune('a'+i)))
			defer db.Close()
			ctx := t.Context()
			account := db.User.GetX(ctx, player.UserID)
			other := db.User.Create().SetEmail("intruder@example.com").SetUID("intruder-uid").SetStatus("active").SaveX(ctx)
			evidence := tc.build(t, db, account, other)

			if _, err := finishPlayerServiceWithEvidence(ctx, db, player.ID, order.ID, 1, 60, []int{evidence.ID}, now.Add(time.Minute)); !errors.Is(err, errInvalidInput) {
				t.Fatalf("非法附件未拒绝：%v", err)
			}
		})
	}
}
