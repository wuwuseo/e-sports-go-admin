package esports

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsagecheck"
	"go-admin/internal/ent/esportscustomer"
)

type ageCheckInput struct {
	ClaimedAgeStatus     string `json:"claimed_age_status"`
	EvidenceAttachmentID int    `json:"evidence_attachment_id"`
}

type ageReviewInput struct {
	Decision                string `json:"decision"`
	ReviewNote              string `json:"review_note"`
	ExpectedCustomerVersion int    `json:"expected_customer_version"`
}

func validEvidenceHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// submitAgeCheck never changes customer eligibility. The evidence must be an
// existing upload by the requesting staff member, with a stable content hash.
func submitAgeCheck(ctx context.Context, db *ent.Client, staff *ent.Admin, customerID int, in ageCheckInput) (*ent.EsportsAgeCheck, error) {
	if staff == nil || customerID < 1 || in.EvidenceAttachmentID < 1 ||
		(in.ClaimedAgeStatus != "adult" && in.ClaimedAgeStatus != "minor") {
		return nil, errInvalidInput
	}
	attachment, err := db.Attachment.Get(ctx, in.EvidenceAttachmentID)
	if err != nil {
		return nil, errInvalidInput
	}
	if attachment.UploadedBy != staff.UID || attachment.Directory != "private/esports/age-checks" ||
		!validEvidenceHash(attachment.ContentHash) ||
		(attachment.MimeType != "image/jpeg" && attachment.MimeType != "image/png" && attachment.MimeType != "application/pdf") {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	customer, err := tx.EsportsCustomer.Get(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if customer.OwnerAdminID == nil || *customer.OwnerAdminID != staff.ID {
		return nil, errForbidden
	}
	check, err := tx.EsportsAgeCheck.Create().SetCustomerID(customerID).
		SetRequestedByAdminID(staff.ID).SetClaimedAgeStatus(esportsagecheck.ClaimedAgeStatus(in.ClaimedAgeStatus)).
		SetVerificationMethod(esportsagecheck.VerificationMethodStaffDocumentReview).
		SetEvidenceAttachmentID(attachment.ID).SetEvidenceSha256(attachment.ContentHash).
		SetPendingKey("customer:" + strconv.Itoa(customerID)).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("customer.age_check_requested:" + strconv.Itoa(check.ID)).
		SetEventType("customer.age_check_requested").SetAggregateRef(strconv.Itoa(customerID)).
		SetPayload(map[string]interface{}{"check_id": check.ID, "customer_id": customerID}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return check, nil
}

// reviewAgeCheck requires a second staff member and a matching customer version.
// The review, eligibility change and event are one database transaction.
func reviewAgeCheck(ctx context.Context, db *ent.Client, reviewer *ent.Admin, checkID int, in ageReviewInput, now time.Time) (*ent.EsportsAgeCheck, error) {
	in.ReviewNote = strings.TrimSpace(in.ReviewNote)
	if reviewer == nil || checkID < 1 || in.ExpectedCustomerVersion < 1 ||
		(in.Decision != "approve" && in.Decision != "reject") ||
		in.ReviewNote == "" || len([]rune(in.ReviewNote)) > 500 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	check, err := tx.EsportsAgeCheck.Get(ctx, checkID)
	if err != nil {
		return nil, err
	}
	if check.Status != esportsagecheck.StatusPending || check.RequestedByAdminID == reviewer.ID {
		return nil, errConflict
	}
	customer, err := tx.EsportsCustomer.Get(ctx, check.CustomerID)
	if err != nil {
		return nil, err
	}
	if customer.Version != in.ExpectedCustomerVersion {
		return nil, errConflict
	}
	if in.Decision == "approve" {
		attachment, err := tx.Attachment.Get(ctx, check.EvidenceAttachmentID)
		if err != nil || attachment.ContentHash != check.EvidenceSha256 {
			return nil, errConflict
		}
		changed, err := tx.EsportsCustomer.Update().Where(
			esportscustomer.IDEQ(customer.ID), esportscustomer.VersionEQ(customer.Version),
		).SetAgeStatus(esportscustomer.AgeStatus(check.ClaimedAgeStatus)).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
	}
	status := esportsagecheck.StatusApproved
	if in.Decision == "reject" {
		status = esportsagecheck.StatusRejected
	}
	changed, err := tx.EsportsAgeCheck.Update().Where(
		esportsagecheck.IDEQ(check.ID), esportsagecheck.StatusEQ(esportsagecheck.StatusPending),
	).SetStatus(status).SetReviewedByAdminID(reviewer.ID).SetReviewNote(in.ReviewNote).
		SetReviewedAt(now.UTC()).ClearPendingKey().Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("customer.age_check_reviewed:" + strconv.Itoa(check.ID)).
		SetEventType("customer.age_check_reviewed").SetAggregateRef(strconv.Itoa(check.CustomerID)).
		SetPayload(map[string]interface{}{"check_id": check.ID, "customer_id": check.CustomerID, "decision": in.Decision}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAgeCheck.Get(ctx, check.ID)
}
