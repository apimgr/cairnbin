package compat

// Coverage tests for uncovered branches in the compat handlers.
// ALL tests share a single Handler to avoid creating more than one SQLite
// DB. Each SQLite DB creation requires several fsyncs; in Docker with a volume
// mount they are slow enough that creating ~18 DBs in one suite exceeds the
// test timeout. One shared DB keeps the suite well inside the limit.
//
// Branches covered:
//   - PastebinRaw: expired paste, burn-after delete
//   - PastebinPost/delete: success with valid token, rejection with wrong token
//   - PastebinPost/list: paste with non-nil ExpiresAt (expireDate branch)
//   - LenGet: expired paste, oneUse body-withheld, oneUse burn-on-open, ExpiresAt → deleteTime
//   - LenRemove: wrong deleteToken → 404
//   - StikkedJSON: expired paste
//   - HastebinGet: expired paste
//   - DpasteCreate: "syntax" fallback, "filename" fallback, positive "expires"
//   - sprungeRespond: empty content (400)
//   - zeroXRespond: non-zero expires (X-Token header)
//   - expiryUnix: nil input → 0; non-nil input → Unix timestamp
//   - writeJSON: SetEscapeHTML(false) preserves literal '&'; non-200 status code

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/handler"
)

// seedExpiredPaste creates a paste whose ExpiresAt is 1 hour in the past so
// that any handler exercising the expiry check will treat it as expired.
func seedExpiredPaste(t *testing.T, ph *handler.PasteHandler, content string) string {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	id, _, err := ph.CreatePasteInternal("expired", content, "text", 0, 0, &past)
	if err != nil {
		t.Fatalf("seedExpiredPaste: %v", err)
	}
	return id
}

// seedBurnAfterPaste creates a paste with burn_after=1.
func seedBurnAfterPaste(t *testing.T, ph *handler.PasteHandler, content string) (id, token string) {
	t.Helper()
	var err error
	id, token, err = ph.CreatePasteInternal("oneuse", content, "text", 0, 1, nil)
	if err != nil {
		t.Fatalf("seedBurnAfterPaste: %v", err)
	}
	return id, token
}

// TestCompatCoverage bundles all compat coverage tests so they share a
// single Handler / SQLite DB. SQLite fsync is slow in Docker; keeping
// DB creation count low avoids hitting the suite timeout.
func TestCompatCoverage(t *testing.T) {
	ch, db := newCompatHandler(t)

	// ── PastebinRaw: expired paste returns 404 and removes the paste ──────────
	t.Run("PastebinRaw_ExpiredPaste", func(t *testing.T) {
		id := seedExpiredPaste(t, ch.ph, "expired raw content")

		req := httptest.NewRequest(http.MethodGet, "/api/api_raw.php?i="+id, nil)
		rr := httptest.NewRecorder()
		ch.PastebinRaw(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404", rr.Code)
		}
		paste, _ := db.GetPasteByID(id)
		if paste != nil {
			t.Error("expired paste should have been deleted from DB")
		}
	})

	// ── PastebinRaw: burn_after=1 deletes paste after first read ──────────────
	t.Run("PastebinRaw_BurnAfter", func(t *testing.T) {
		id, _ := seedBurnAfterPaste(t, ch.ph, "burns on read")

		req := httptest.NewRequest(http.MethodGet, "/api/api_raw.php?i="+id, nil)
		rr := httptest.NewRecorder()
		ch.PastebinRaw(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("first read status: got %d, want 200", rr.Code)
		}
		if rr.Body.String() != "burns on read" {
			t.Errorf("body: got %q, want %q", rr.Body.String(), "burns on read")
		}
		paste, _ := db.GetPasteByID(id)
		if paste != nil {
			t.Error("burn_after=1 paste should be deleted after first read")
		}
	})

	// ── PastebinPost/delete: valid token removes the paste ────────────────────
	t.Run("PastebinDelete_Success", func(t *testing.T) {
		id, token := seedPaste(t, ch.ph, "to be deleted")

		form := url.Values{
			"api_option":    {"delete"},
			"api_paste_key": {id},
			"api_user_key":  {token},
		}
		req := httptest.NewRequest(http.MethodPost, "/api/api_post.php",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		ch.PastebinPost(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "Removed") {
			t.Errorf("body: got %q, want 'Paste Removed'", rr.Body.String())
		}
		paste, _ := db.GetPasteByID(id)
		if paste != nil {
			t.Error("paste should not exist in DB after successful delete")
		}
	})

	// ── PastebinPost/delete: wrong token → 404 ────────────────────────────────
	t.Run("PastebinDelete_InvalidToken", func(t *testing.T) {
		id, _ := seedPaste(t, ch.ph, "not deleted")

		form := url.Values{
			"api_option":    {"delete"},
			"api_paste_key": {id},
			"api_user_key":  {"wrongtoken00000"},
		}
		req := httptest.NewRequest(http.MethodPost, "/api/api_post.php",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		ch.PastebinPost(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404", rr.Code)
		}
	})

	// ── PastebinPost/list: paste with non-nil ExpiresAt ───────────────────────
	t.Run("PastebinList_WithExpiry", func(t *testing.T) {
		future := time.Now().Add(24 * time.Hour)
		_, _, err := ch.ph.CreatePasteInternal("expiring", "content with expiry", "text", 0, 0, &future)
		if err != nil {
			t.Fatalf("create paste: %v", err)
		}

		form := url.Values{"api_option": {"list"}}
		req := httptest.NewRequest(http.MethodPost, "/api/api_post.php",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		ch.PastebinPost(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200", rr.Code)
		}
		body := rr.Body.String()
		if !strings.Contains(body, "<paste>") {
			t.Error("expected at least one <paste> element in XML response")
		}
		if !strings.Contains(body, "<paste_expire_date>") {
			t.Error("expected <paste_expire_date> tag in XML")
		}
	})

	// ── LenGet: expired paste → 404 + deletion ────────────────────────────────
	t.Run("LenGet_ExpiredPaste", func(t *testing.T) {
		id := seedExpiredPaste(t, ch.ph, "expired len content")

		req := httptest.NewRequest(http.MethodGet, "/api/get?id="+id, nil)
		rr := httptest.NewRecorder()
		ch.LenGet(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404", rr.Code)
		}
		paste, _ := db.GetPasteByID(id)
		if paste != nil {
			t.Error("LenGet should delete an expired paste from the DB")
		}
	})

	// ── LenGet: burn_after=1 without openOneUse → body withheld ──────────────
	t.Run("LenGet_OneUse_WithheldBody", func(t *testing.T) {
		id, _ := seedBurnAfterPaste(t, ch.ph, "secret one-use")

		req := httptest.NewRequest(http.MethodGet, "/api/get?id="+id, nil)
		rr := httptest.NewRecorder()
		ch.LenGet(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
		}
		var resp map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if _, exists := resp["body"]; exists {
			t.Error("body field should not be present when oneUse paste is not opened")
		}
		if resp["oneUse"] != true {
			t.Errorf("oneUse: got %v, want true", resp["oneUse"])
		}
		if resp["id"] == "" || resp["id"] == nil {
			t.Error("id field should be present in withheld-body response")
		}
	})

	// ── LenGet: openOneUse=true → body returned and paste deleted ─────────────
	t.Run("LenGet_OneUse_OpenBody", func(t *testing.T) {
		id, _ := seedBurnAfterPaste(t, ch.ph, "one-use content")

		req := httptest.NewRequest(http.MethodGet, "/api/get?id="+id+"&openOneUse=true", nil)
		rr := httptest.NewRecorder()
		ch.LenGet(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
		}
		var resp map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp["body"] != "one-use content" {
			t.Errorf("body: got %v, want %q", resp["body"], "one-use content")
		}
		paste, _ := db.GetPasteByID(id)
		if paste != nil {
			t.Error("burn_after=1 paste should be deleted after openOneUse=true read")
		}
	})

	// ── LenGet: paste with ExpiresAt → non-zero deleteTime in response ────────
	t.Run("LenGet_ExpiryUnixInResponse", func(t *testing.T) {
		future := time.Now().Add(24 * time.Hour)
		id, _, err := ch.ph.CreatePasteInternal("", "has expiry", "text", 0, 0, &future)
		if err != nil {
			t.Fatalf("create paste: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/api/get?id="+id, nil)
		rr := httptest.NewRecorder()
		ch.LenGet(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200", rr.Code)
		}
		var resp map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		deleteTime, _ := resp["deleteTime"].(float64)
		if deleteTime == 0 {
			t.Errorf("deleteTime: got 0, want a non-zero Unix timestamp for paste with ExpiresAt")
		}
	})

	// ── LenRemove: wrong token → 404 ─────────────────────────────────────────
	t.Run("LenRemove_InvalidToken", func(t *testing.T) {
		id, _ := seedPaste(t, ch.ph, "remove me")

		req := httptest.NewRequest(http.MethodDelete,
			"/api/remove?id="+id+"&deleteToken=totally-wrong-token", nil)
		rr := httptest.NewRecorder()
		ch.LenRemove(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404", rr.Code)
		}
	})

	// ── StikkedJSON: expired paste → 404 + deletion ───────────────────────────
	t.Run("StikkedJSON_ExpiredPaste", func(t *testing.T) {
		id := seedExpiredPaste(t, ch.ph, "stikked expired content")

		req := withID(httptest.NewRequest(http.MethodGet, "/api/paste/"+id, nil), id)
		rr := httptest.NewRecorder()
		ch.StikkedJSON(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404", rr.Code)
		}
		paste, _ := db.GetPasteByID(id)
		if paste != nil {
			t.Error("StikkedJSON should delete an expired paste from the DB")
		}
	})

	// ── HastebinGet: expired paste → 404 + deletion ───────────────────────────
	t.Run("HastebinGet_ExpiredPaste", func(t *testing.T) {
		id := seedExpiredPaste(t, ch.ph, "hastebin expired content")

		req := withID(httptest.NewRequest(http.MethodGet, "/documents/"+id, nil), id)
		rr := httptest.NewRecorder()
		ch.HastebinGet(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404", rr.Code)
		}
		paste, _ := db.GetPasteByID(id)
		if paste != nil {
			t.Error("HastebinGet should delete an expired paste from the DB")
		}
	})

	// ── DpasteCreate: "syntax" field used when "lexer" is absent ─────────────
	t.Run("DpasteCreate_SyntaxFieldFallback", func(t *testing.T) {
		form := url.Values{"content": {"print('hi')"}, "syntax": {"python"}, "format": {"json"}}
		req := httptest.NewRequest(http.MethodPost, "/api/",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		ch.DpasteCreate(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
		}
		var resp map[string]string
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp["lexer"] != "python" {
			t.Errorf("lexer: got %q, want %q", resp["lexer"], "python")
		}
	})

	// ── DpasteCreate: "filename" field used when both "lexer" and "syntax" absent
	t.Run("DpasteCreate_FilenameFieldFallback", func(t *testing.T) {
		form := url.Values{"content": {"#!/bin/bash\necho hi"}, "filename": {"script.sh"}, "format": {"json"}}
		req := httptest.NewRequest(http.MethodPost, "/api/",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		ch.DpasteCreate(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
		}
		var resp map[string]string
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp["lexer"] != "script.sh" {
			t.Errorf("lexer: got %q, want %q", resp["lexer"], "script.sh")
		}
	})

	// ── DpasteCreate: positive "expires" value sets a future ExpiresAt ────────
	t.Run("DpasteCreate_WithExpires", func(t *testing.T) {
		form := url.Values{"content": {"expires in 7 days"}, "expires": {"7"}}
		req := httptest.NewRequest(http.MethodPost, "/api/",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		ch.DpasteCreate(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
		}
	})

	// ── sprungeRespond: blank content → 400 ─────────────────────────────────
	t.Run("SprungeRespond_EmptyContent", func(t *testing.T) {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		ch.sprungeRespond(rr, r, "   ")
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400", rr.Code)
		}
	})

	// ── zeroXRespond: non-zero expires → /raw/ URL + X-Token ────────────────
	t.Run("ZeroXRespond_WithExpiry", func(t *testing.T) {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		ch.zeroXRespond(rr, r, "expiry content", "24")
		if rr.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200\nbody: %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "/raw/") {
			t.Errorf("body: got %q, want a /raw/ URL", rr.Body.String())
		}
		if rr.Header().Get("X-Token") == "" {
			t.Error("X-Token header should be set for a 0x0.st-style upload")
		}
	})
}

// ─── Pure-function tests (no DB needed) ───────────────────────────────────────

// TestExpiryUnix verifies both the nil and non-nil paths of the expiryUnix helper.
func TestExpiryUnix(t *testing.T) {
	if got := expiryUnix(nil); got != 0 {
		t.Errorf("expiryUnix(nil) = %d, want 0", got)
	}

	now := time.Now().Truncate(time.Second)
	if got := expiryUnix(&now); got != now.Unix() {
		t.Errorf("expiryUnix(&now) = %d, want %d", got, now.Unix())
	}
}

// TestWriteJSON_NoHTMLEscape verifies that writeJSON does not HTML-escape '&'.
// SetEscapeHTML(false) must be set; if it were true, '&' becomes '&' which
// breaks URL consumers.
func TestWriteJSON_NoHTMLEscape(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusOK, map[string]string{"url": "http://x.com/?a=1&b=2"})

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	// With SetEscapeHTML(false), the JSON encoder must keep & as a literal
	// ampersand — it must NOT produce the 6-char unicode escape &.
	if strings.Contains(body, "\\u0026") {
		t.Errorf("writeJSON HTML-escaped '&' to \\u0026; SetEscapeHTML(false) should prevent this: %s", body)
	}
	if !strings.Contains(body, "&") {
		t.Errorf("writeJSON did not preserve literal '&' in JSON values: %s", body)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
}

// TestWriteJSON_NonOKStatus verifies that writeJSON sets the supplied HTTP status.
func TestWriteJSON_NonOKStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusNotFound, map[string]string{"error": "not found"})
	if rr.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
}
