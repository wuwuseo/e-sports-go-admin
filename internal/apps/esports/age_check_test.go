package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/service"
)

func TestAgeCheckRequiresEvidenceAndIndependentReview(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_age_check?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	staff := db.Admin.Create().SetEmail("staff@example.com").SetPassword("test").SetUID("age-staff").
		SetUsername("age-staff").SetStatus("active").SaveX(ctx)
	reviewer := db.Admin.Create().SetEmail("reviewer@example.com").SetPassword("test").SetUID("age-reviewer").
		SetUsername("age-reviewer").SetStatus("active").SaveX(ctx)
	customer, err := saveCustomer(ctx, db, staff.ID, customerInput{DisplayName: "待核验客户"})
	if err != nil {
		t.Fatal(err)
	}
	evidence := db.Attachment.Create().SetName("证据").SetOriginalName("proof.png").
		SetPath("private/age/proof.png").SetMimeType("image/png").SetExtension("png").
		SetDirectory("private/esports/age-checks").SetSize(128).SetUploadedBy(staff.UID).
		SetContentHash(strings.Repeat("a", 64)).SaveX(ctx)
	if _, err := submitAgeCheck(ctx, db, staff, customer.ID, ageCheckInput{
		ClaimedAgeStatus: "adult", EvidenceAttachmentID: evidence.ID + 1,
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("missing evidence accepted: %v", err)
	}
	check, err := submitAgeCheck(ctx, db, staff, customer.ID, ageCheckInput{
		ClaimedAgeStatus: "adult", EvidenceAttachmentID: evidence.ID,
	})
	if err != nil || check.Status != "pending" || db.EsportsCustomer.GetX(ctx, customer.ID).AgeStatus != "unknown" {
		t.Fatalf("submission changed eligibility: %+v %v", check, err)
	}
	attachmentService := &service.AttachmentService{CRUDBase: &service.CRUDBase{Client: db}}
	if err := attachmentService.Delete(ctx, evidence.ID); !errors.Is(err, service.ErrAttachmentDeleteFailed) {
		t.Fatalf("referenced evidence was deletable: %v", err)
	}
	if err := attachmentService.BatchDelete(ctx, []int{evidence.ID}); !errors.Is(err, service.ErrAttachmentDeleteFailed) {
		t.Fatalf("referenced evidence was batch-deletable: %v", err)
	}
	if _, err := submitAgeCheck(ctx, db, staff, customer.ID, ageCheckInput{
		ClaimedAgeStatus: "minor", EvidenceAttachmentID: evidence.ID,
	}); !errors.Is(err, errConflict) {
		t.Fatalf("second pending check accepted: %v", err)
	}
	now := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	input := ageReviewInput{Decision: "approve", ReviewNote: "已核对原始证据", ExpectedCustomerVersion: customer.Version}
	if _, err := reviewAgeCheck(ctx, db, staff, check.ID, input, now); !errors.Is(err, errConflict) {
		t.Fatalf("self approval accepted: %v", err)
	}
	input.ExpectedCustomerVersion++
	if _, err := reviewAgeCheck(ctx, db, reviewer, check.ID, input, now); !errors.Is(err, errConflict) {
		t.Fatalf("stale customer version accepted: %v", err)
	}
	input.ExpectedCustomerVersion--
	reviewed, err := reviewAgeCheck(ctx, db, reviewer, check.ID, input, now)
	if err != nil || reviewed.Status != "approved" || db.EsportsCustomer.GetX(ctx, customer.ID).AgeStatus != "adult" {
		t.Fatalf("approved check did not update eligibility: %+v %v", reviewed, err)
	}
	if _, err := reviewAgeCheck(ctx, db, reviewer, check.ID, input, now); !errors.Is(err, errConflict) {
		t.Fatalf("duplicate review accepted: %v", err)
	}
	next, err := submitAgeCheck(ctx, db, staff, customer.ID, ageCheckInput{
		ClaimedAgeStatus: "minor", EvidenceAttachmentID: evidence.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reviewAgeCheck(ctx, db, reviewer, next.ID, ageReviewInput{
		Decision: "reject", ReviewNote: "证据与客户不符", ExpectedCustomerVersion: customer.Version + 1,
	}, now.Add(time.Minute))
	if err != nil || db.EsportsCustomer.GetX(ctx, customer.ID).AgeStatus != "adult" ||
		db.EsportsOutbox.Query().CountX(ctx) != 4 {
		t.Fatalf("rejection changed eligibility or lost audit event: %v", err)
	}
}
