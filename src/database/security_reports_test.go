package database_test

import (
	"errors"
	"strings"
	"testing"
	"time"

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

func TestReencryptPGPSecurityReportsIsAtomic(t *testing.T) {
	db := newTestDB(t)
	pgpOne := sampleReport("sec_pgp_one_12345678")
	pgpOne.EncMethod = "pgp"
	pgpOne.EncryptedBody = []byte("one")
	pgpTwo := sampleReport("sec_pgp_two_12345678")
	pgpTwo.EncMethod = "pgp"
	pgpTwo.EncryptedBody = []byte("two")
	aes := sampleReport("sec_aes_three_123456")
	for _, report := range []*database.SecurityReport{pgpOne, pgpTwo, aes} {
		if err := db.CreateSecurityReport(report); err != nil {
			t.Fatal(err)
		}
	}
	rotator := db.(interface {
		ReencryptPGPSecurityReports(func([]byte) ([]byte, error)) (int, error)
	})
	visits := 0
	if _, err := rotator.ReencryptPGPSecurityReports(func(body []byte) ([]byte, error) {
		visits++
		if visits == 2 {
			return nil, errors.New("cannot decrypt")
		}
		return []byte("new:" + string(body)), nil
	}); err == nil {
		t.Fatal("expected failed conversion to abort the transaction")
	}
	for _, want := range []*database.SecurityReport{pgpOne, pgpTwo, aes} {
		got, err := db.GetSecurityReport(want.TrackingID)
		if err != nil {
			t.Fatal(err)
		}
		if string(got.EncryptedBody) != string(want.EncryptedBody) {
			t.Errorf("report %s changed after rollback: %q", want.TrackingID, got.EncryptedBody)
		}
	}
	updated, err := rotator.ReencryptPGPSecurityReports(func(body []byte) ([]byte, error) {
		return []byte("new:" + string(body)), nil
	})
	if err != nil || updated != 2 {
		t.Fatalf("successful rotation = (%d, %v), want (2, nil)", updated, err)
	}
	got, err := db.GetSecurityReport(pgpOne.TrackingID)
	if err != nil || !strings.HasPrefix(string(got.EncryptedBody), "new:") {
		t.Errorf("PGP body was not re-encrypted: %q, %v", got.EncryptedBody, err)
	}
	got, err = db.GetSecurityReport(aes.TrackingID)
	if err != nil || string(got.EncryptedBody) != string(aes.EncryptedBody) {
		t.Errorf("AES body changed during PGP rotation: %q, %v", got.EncryptedBody, err)
	}
}

func TestSecurityReportRetentionDeletesClosedOnly(t *testing.T) {
	db := newTestDB(t)
	retentionDB := db.(interface {
		DeleteExpiredSecurityReports(time.Time) (int64, error)
	})
	closed := sampleReport("sec_closed_123456789")
	open := sampleReport("sec_open_1234567890")
	for _, report := range []*database.SecurityReport{closed, open} {
		if err := db.CreateSecurityReport(report); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpdateSecurityReportStatus(closed.TrackingID, database.SecStatusWontFix, "closed"); err != nil {
		t.Fatal(err)
	}
	closedReport, err := db.GetSecurityReport(closed.TrackingID)
	if err != nil || closedReport.ClosedAt == nil {
		t.Fatalf("closed_at missing after terminal status: %+v, %v", closedReport, err)
	}
	deleted, err := retentionDB.DeleteExpiredSecurityReports(time.Now().Add(time.Second))
	if err != nil || deleted != 1 {
		t.Fatalf("delete expired reports = (%d, %v), want (1, nil)", deleted, err)
	}
	got, err := db.GetSecurityReport(open.TrackingID)
	if err != nil || got == nil {
		t.Errorf("open report was deleted: %+v, %v", got, err)
	}
	got, err = db.GetSecurityReport(closed.TrackingID)
	if err != nil || got != nil {
		t.Errorf("closed report was retained: %+v, %v", got, err)
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
	if err := db.CreateSecurityReport(a); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSecurityReportStatus(a.TrackingID, database.SecStatusDisclosed, ""); err != nil {
		t.Fatal(err)
	}
	// Opted out of credit → excluded even when disclosed.
	b := sampleReport("sec_nocreditbbbbbbbb")
	b.CreditPreference = "no"
	if err := db.CreateSecurityReport(b); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSecurityReportStatus(b.TrackingID, database.SecStatusDisclosed, ""); err != nil {
		t.Fatal(err)
	}
	// Credited but not yet disclosed → excluded.
	c := sampleReport("sec_pendingcccccccc")
	if err := db.CreateSecurityReport(c); err != nil {
		t.Fatal(err)
	}

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
