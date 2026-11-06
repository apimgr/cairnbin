package compat

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/handler"
	"github.com/apimgr/cairnbin/src/metric"
	"github.com/apimgr/cairnbin/src/model"
)

// ─── pastebin.com compatibility ───────────────────────────────────────────────

// PastebinPost handles POST /api/api_post.php (pastebin.com API create)
//
// Accepted form fields (subset):
//
//	api_paste_code       — content (required)
//	api_paste_name       — title (optional)
//	api_paste_format     — language (optional)
//	api_paste_private    — 0=public, 1=unlisted, 2=private (treat 2 as unlisted)
//	api_paste_expire_date — N/A/10M/1H/1D/1W/2W/1M/6M/1Y → expiry
//	api_dev_key          — silently ignored
//	api_user_key         — silently ignored
//
// PastebinPost handles POST /api/api_post.php — dispatches on api_option.
//
//	api_option=paste        — create paste (default when field absent)
//	api_option=list         — list recent public pastes as XML
//	api_option=delete       — delete paste by api_paste_key using api_user_key as token
//	api_option=userdetails  — return stub XML user record
func (c *Handler) PastebinPost(w http.ResponseWriter, r *http.Request) {
	if err := c.parseFormLimited(w, r); err != nil {
		if errors.Is(err, errCompatBodyTooLarge) {
			http.Error(w, "Bad API request, paste exceeds the maximum allowed size.", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Bad Request | invalid form", http.StatusBadRequest)
		return
	}

	switch r.FormValue("api_option") {
	case "list":
		c.pastebinList(w, r)
	case "delete":
		c.pastebinDelete(w, r)
	case "userdetails":
		c.pastebinUserDetails(w, r)
	default:
		// "paste" or empty
		c.pastebinCreate(w, r)
	}
}

// pastebinCreate handles api_option=paste.
func (c *Handler) pastebinCreate(w http.ResponseWriter, r *http.Request) {
	content := r.FormValue("api_paste_code")
	if strings.TrimSpace(content) == "" {
		http.Error(w, "Bad API request, the value you use for 'api_paste_code' is empty.", http.StatusBadRequest)
		return
	}

	title := r.FormValue("api_paste_name")
	lang := r.FormValue("api_paste_format")
	if lang == "" {
		lang = "text"
	}

	vis := model.VisibilityPublic
	if priv := r.FormValue("api_paste_private"); priv == "1" || priv == "2" {
		vis = model.VisibilityUnlisted
	}

	expiresAt := parsePastebinExpiry(r.FormValue("api_paste_expire_date"))

	pasteID, _, err := c.ph.CreatePasteInternal(title, content, lang, vis, 0, expiresAt)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	link := c.ph.PasteURL(r, pasteID)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprint(w, link)
}

// pastebinList handles api_option=list — returns XML paste list.
// Honours api_results_limit (1-1000, default 50).
func (c *Handler) pastebinList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.FormValue("api_results_limit"))
	if limit < 1 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}

	pastes, _, err := c.db.GetPublicPastes(1, limit)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = fmt.Fprint(w, "<pastes>")
	for _, p := range pastes {
		expireDate := "0"
		if p.ExpiresAt != nil {
			expireDate = strconv.FormatInt(p.ExpiresAt.Unix(), 10)
		}
		_, _ = fmt.Fprintf(w,
			"<paste><paste_key>%s</paste_key><paste_title>%s</paste_title>"+
				"<paste_date>%d</paste_date><paste_expire_date>%s</paste_expire_date>"+
				"<paste_hits>%d</paste_hits><paste_private>0</paste_private></paste>",
			xmlEscape(p.ID), xmlEscape(p.Title), p.CreatedAt.Unix(), expireDate,
			p.Views,
		)
	}
	_, _ = fmt.Fprint(w, "</pastes>")
}

// pastebinDelete handles api_option=delete.
// api_paste_key is the paste ID; api_user_key is treated as the delete token
// when non-empty and not "ANONYMOUS".
func (c *Handler) pastebinDelete(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("api_paste_key")
	if id == "" {
		http.Error(w, "Bad API request, you need to be logged in to delete a paste.", http.StatusBadRequest)
		return
	}

	token := r.FormValue("api_user_key")
	if token == "" || token == "ANONYMOUS" {
		http.Error(w, "Bad API request, you are not authorized to delete this paste.", http.StatusForbidden)
		return
	}

	if err := c.db.DeletePasteByToken(id, handler.HashToken(token)); err != nil {
		http.Error(w, "Bad API request, invalid paste ID.", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprint(w, "Paste Removed")
}

// pastebinUserDetails handles api_option=userdetails — returns stub XML.
func (c *Handler) pastebinUserDetails(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = fmt.Fprint(w, `<user>`+
		`<user_name>anonymous</user_name>`+
		`<user_email></user_email>`+
		`<user_website></user_website>`+
		`<user_avatar_url></user_avatar_url>`+
		`<user_location></user_location>`+
		`<user_account_type>0</user_account_type>`+
		`<user_private>0</user_private>`+
		`<user_format_short>text</user_format_short>`+
		`<user_expiration>N</user_expiration>`+
		`</user>`,
	)
}

// xmlEscape replaces the five XML special characters.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}

// PastebinRaw handles GET /api/api_raw.php?i={id}
func (c *Handler) PastebinRaw(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("i")
	if id == "" {
		http.Error(w, "Bad API request", http.StatusBadRequest)
		return
	}

	paste, err := c.db.GetPasteByID(id)
	if err != nil || paste == nil {
		http.Error(w, "Bad API request, invalid paste ID", http.StatusNotFound)
		return
	}
	if paste.ExpiresAt != nil && paste.ExpiresAt.Before(time.Now()) {
		_ = c.db.DeletePaste(id)
		http.Error(w, "Bad API request, invalid paste ID", http.StatusNotFound)
		return
	}

	if _, burned, verr := c.db.IncrementViewsAndCheckBurn(id); verr == nil && burned {
		c.ph.InvalidatePasteCache(id)
		metric.PastesDeletedTotal.Inc()
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprint(w, paste.Content)
}

// PastebinLogin handles POST /api/api_login.php — always returns "ANONYMOUS"
// because this instance has no user accounts.
func (c *Handler) PastebinLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprint(w, "ANONYMOUS")
}

// parsePastebinExpiry maps pastebin.com expire_date codes to a time.Time.
func parsePastebinExpiry(code string) *time.Time {
	var d time.Duration
	switch code {
	case "10M":
		d = 10 * time.Minute
	case "1H":
		d = time.Hour
	case "1D":
		d = 24 * time.Hour
	case "1W":
		d = 7 * 24 * time.Hour
	case "2W":
		d = 14 * 24 * time.Hour
	case "1M":
		d = 30 * 24 * time.Hour
	case "6M":
		d = 180 * 24 * time.Hour
	case "1Y":
		d = 365 * 24 * time.Hour
	default:
		// "N" (never) or unknown
		return nil
	}
	t := time.Now().Add(d)
	return &t
}
