package compat

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/data"
	"github.com/apimgr/cairnbin/src/handler"
	"github.com/apimgr/cairnbin/src/metric"
	"github.com/apimgr/cairnbin/src/model"
)

// ─── lenpaste compatibility ───────────────────────────────────────────────────

// LenCreate handles POST /api/new
//
// lenpaste form fields (per forksmgr/lcomrade-lenpaste's
// netshare.PasteAddFromForm wire protocol):
//
//	title          — paste title
//	body           — content
//	syntax         — language
//	expiration     — seconds until expiry (0/absent=never)
//	oneUse         — "true" maps to burn_after=1
//	createTokenHash — ignored (public instance)
func (c *Handler) LenCreate(w http.ResponseWriter, r *http.Request) {
	if err := c.parseFormLimited(w, r); err != nil {
		if errors.Is(err, errCompatBodyTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body exceeds maximum paste size"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}

	content := r.FormValue("body")
	if strings.TrimSpace(content) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body is required"})
		return
	}

	title := r.FormValue("title")
	lang := r.FormValue("syntax")
	if lang == "" {
		lang = "text"
	}

	burn := 0
	if r.FormValue("oneUse") == "true" {
		burn = 1
	}

	var expiresAt *time.Time
	if lt, err := strconv.ParseInt(r.FormValue("expiration"), 10, 64); err == nil && lt > 0 {
		t := time.Now().Add(time.Duration(lt) * time.Second)
		expiresAt = &t
	}

	pasteID, deleteToken, err := c.ph.CreatePasteInternal(title, content, lang, model.VisibilityPublic, burn, expiresAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create paste"})
		return
	}

	link := c.ph.PasteURL(r, pasteID)
	writeJSON(w, http.StatusOK, map[string]string{
		"id":          pasteID,
		"url":         link,
		"deleteToken": deleteToken,
	})
}

// LenGet handles GET /api/get?id={id}&openOneUse=true
//
// When a paste has burn_after==1 and openOneUse is NOT set to "true", only
// {"id":"...","oneUse":true} is returned (body withheld), consistent with
// lenpaste behaviour for one-time pastes.
func (c *Handler) LenGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}

	paste, err := c.db.GetPasteByID(id)
	if err != nil || paste == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "paste not found"})
		return
	}
	if paste.ExpiresAt != nil && paste.ExpiresAt.Before(time.Now()) {
		c.db.DeletePaste(id)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "paste has expired"})
		return
	}

	// Withhold body for one-time pastes unless the caller explicitly
	// acknowledges they want to consume it (openOneUse=true).
	if paste.BurnAfter == 1 && r.URL.Query().Get("openOneUse") != "true" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"id":     paste.ID,
			"oneUse": true,
		})
		return
	}

	views, burned, verr := c.db.IncrementViewsAndCheckBurn(id)
	if verr == nil {
		paste.Views = views
	} else {
		paste.Views++
	}

	if burned {
		c.ph.InvalidatePasteCache(id)
		metric.PastesDeletedTotal.Inc()
	}

	paste.DeleteTokenHash = ""
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":         paste.ID,
		"title":      paste.Title,
		"body":       paste.Content,
		"syntax":     paste.Language,
		"oneUse":     paste.BurnAfter == 1,
		"createTime": paste.CreatedAt.Unix(),
		"deleteTime": expiryUnix(paste.ExpiresAt),
		"views":      paste.Views,
	})
}

// LenRemove handles DELETE /api/remove?id={id}&deleteToken={token}
func (c *Handler) LenRemove(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	token := r.URL.Query().Get("deleteToken")

	if id == "" || token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id and deleteToken are required"})
		return
	}

	if err := c.db.DeletePasteByToken(id, handler.HashToken(token)); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "paste not found or invalid token"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "paste deleted"})
}

// LenList handles GET /api/list?pageSize=N&page=N
func (c *Handler) LenList(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if limit < 1 || limit > 100 {
		limit = 20
	}

	pastes, total, err := c.db.GetPublicPastes(page, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to fetch"})
		return
	}

	type lenItem struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		Syntax     string `json:"syntax"`
		CreateTime int64  `json:"createTime"`
		Views      int    `json:"views"`
	}

	items := make([]lenItem, 0, len(pastes))
	for _, p := range pastes {
		items = append(items, lenItem{
			ID:         p.ID,
			Title:      p.Title,
			Syntax:     p.Language,
			CreateTime: p.CreatedAt.Unix(),
			Views:      p.Views,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pastes": items,
		"count":  total,
	})
}

// LenServerInfo handles GET /api/v1/getServerInfo (lenpaste compat).
func (c *Handler) LenServerInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":        c.version,
		"titleMaxlength": 100,
		"bodyMaxlength":  10 * 1024 * 1024,
		"maxLifeTime":    -1,
		"serverAbout":    "",
		"serverRules":    "",
		"adminName":      "",
		"adminMail":      "",
		"syntaxes":       data.Languages(),
	})
}
