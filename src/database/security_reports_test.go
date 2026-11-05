package database_test

import (
	"testing"

	"github.com/apimgr/cairnbin/src/database"
)

func sampleReport(id string) *database.SecurityReport {
	return &database.SecurityReport{
		TrackingID:       id,
		Severity:         "High",
		Component:        "api",
		EncryptedBody:    []byte{0x01, 0x02, 0x03},
		EncMethod:        "aes-256-gcm",
		CreditPreference: "handle",
		CreditName:       "acidburn",
		TokenHash:        "deadbeef",
		DisclosureDays:   90,
	}
}

func TestSecurityReportRoundTrip(t *testing.T) {
	db := newTestDB(t)
	r := sampleReport("sec_0123456789abcdef")
	if err := db.CreateSecurityReport(r); err != nil {
		t.Fatalf("CreateSecurityReport: %v", err)
	}
	got, err := db.GetSecurityReport(r.TrackingID)
	if err != nil {
		t.Fatalf("GetSecurityReport: %v", err)
	}
	if got == nil {
		t.Fatal("expected report, got nil")
	}
	if got.Status != database.SecStatusReceived {
		t.Errorf("status = %q, want %q", got.Status, database.SecStatusReceived)
	}
	if string(got.EncryptedBody) != string(r.EncryptedBody) {
		t.Errorf("encrypted body mismatch")
	}
	if got.Component != "api" || got.Severity != "High" {
		t.Errorf("metadata mismatch: %+v", got)
	}
}

func TestSecurityReportNotFound(t *testing.T) {
	db := newTestDB(t)
	got, err := db.GetSecurityReport("sec_missing")
	if err != nil {
		t.Fatalf("GetSecurityReport: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing report, got %+v", got)
	}
}

func TestSecurityReportStatusUpdateAndDisclosure(t *testing.T) {
	db := newTestDB(t)
	r := sampleReport("sec_aaaabbbbccccdddd")
	if err := db.CreateSecurityReport(r); err != nil {
		t.Fatalf("CreateSecurityReport: %v", err)
	}
	if err := db.UpdateSecurityReportStatus(r.TrackingID, database.SecStatusTriaged, "under review"); err != nil {
		t.Fatalf("UpdateSecurityReportStatus: %v", err)
	}
	got, _ := db.GetSecurityReport(r.TrackingID)
	if got.Status != database.SecStatusTriaged || got.MaintainerComment != "under review" {
		t.Errorf("update not applied: %+v", got)
	}
	if got.DisclosedAt != nil {
		t.Error("disclosed_at should be nil before disclosure")
	}
	if err := db.UpdateSecurityReportStatus(r.TrackingID, database.SecStatusDisclosed, "public now"); err != nil {
		t.Fatalf("disclose: %v", err)
	}
	got, _ = db.GetSecurityReport(r.TrackingID)
	if got.DisclosedAt == nil {
		t.Error("disclosed_at should be set after disclosure")
	}
}

func TestListDisclosedSecurityReports(t *testing.T) {
	db := newTestDB(t)
	// Credited + disclosed → listed.
	a := sampleReport("sec_creditedaaaaaaaa")
	db.CreateSecurityReport(a)
	db.UpdateSecurityReportStatus(a.TrackingID, database.SecStatusDisclosed, "")
	// Opted out of credit → excluded even when disclosed.
	b := sampleReport("sec_nocreditbbbbbbbb")
	b.CreditPreference = "no"
	db.CreateSecurityReport(b)
	db.UpdateSecurityReportStatus(b.TrackingID, database.SecStatusDisclosed, "")
	// Credited but not yet disclosed → excluded.
	c := sampleReport("sec_pendingcccccccc")
	db.CreateSecurityReport(c)

	list, err := db.ListDisclosedSecurityReports()
	if err != nil {
		t.Fatalf("ListDisclosedSecurityReports: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 listed report, got %d", len(list))
	}
	if list[0].TrackingID != a.TrackingID {
		t.Errorf("listed wrong report: %q", list[0].TrackingID)
	}
}
