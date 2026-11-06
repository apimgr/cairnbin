package compat

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apimgr/cairnbin/src/handler"
	"github.com/apimgr/cairnbin/src/metric"
	"github.com/apimgr/cairnbin/src/model"
)

type microbinCreateFields struct {
	Content    string `json:"content"`
	Title      string `json:"title"`
	Language   string `json:"language"`
	Visibility string `json:"visibility"`
	Expiry     string `json:"expiry"`
	BurnAfter  int    `json:"burn_after"`
}

// MicrobinCreate implements the MicroBin-compatible create endpoint. It uses
// the compatibility delete token and never creates native owner credentials.
func (c *Handler) MicrobinCreate(w http.ResponseWriter, r *http.Request) {
	limit := c.ph.EffectiveReadLimit()
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	var body microbinCreateFields
	contentType := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(contentType, "application/json"):
		raw, err := io.ReadAll(r.Body)
		if err != nil || int64(len(raw)) > limit {
			microbinError(w, http.StatusRequestEntityTooLarge, "content exceeds maximum size", r)
			return
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			microbinError(w, http.StatusBadRequest, "invalid JSON", r)
			return
		}
	case strings.HasPrefix(contentType, "multipart/form-data"):
		if err := r.ParseMultipartForm(32 << 10); err != nil {
			if isMaxBytesErr(err) {
				microbinError(w, http.StatusRequestEntityTooLarge, "content exceeds maximum size", r)
			} else {
				microbinError(w, http.StatusBadRequest, "invalid form", r)
			}
			return
		}
		microbinFieldsFromForm(r, &body)
		if body.Content == "" {
			file, _, err := r.FormFile("file")
			if err != nil {
				file, _, err = r.FormFile("files")
			}
			if err == nil {
				defer func() { _ = file.Close() }()
				content, err := io.ReadAll(io.LimitReader(file, limit+1))
				if err != nil || int64(len(content)) > limit {
					microbinError(w, http.StatusRequestEntityTooLarge, "content exceeds maximum size", r)
					return
				}
				body.Content = string(content)
			}
		}
	case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
		if err := c.parseFormLimited(w, r); err != nil {
			if errors.Is(err, errCompatBodyTooLarge) {
				microbinError(w, http.StatusRequestEntityTooLarge, "content exceeds maximum size", r)
			} else {
				microbinError(w, http.StatusBadRequest, "invalid form", r)
			}
			return
		}
		microbinFieldsFromForm(r, &body)
	default:
		microbinError(w, http.StatusUnsupportedMediaType, "unsupported content type", r)
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		microbinError(w, http.StatusBadRequest, "content is required", r)
		return
	}

	visibility := model.VisibilityPublic
	if body.Visibility == "unlisted" || body.Visibility == "private" || body.Visibility == "1" {
		visibility = model.VisibilityUnlisted
	}
	if body.BurnAfter < 0 {
		body.BurnAfter = 0
	}
	if body.BurnAfter > 9999 {
		body.BurnAfter = 9999
	}
	var expiresAt *time.Time
	if body.Expiry != "" && body.Expiry != "never" {
		expiresAt = handler.ParseExpiry(body.Expiry)
	}
	id, deleteToken, err := c.ph.CreatePasteInternal(body.Title, body.Content, body.Language, visibility, body.BurnAfter, expiresAt)
	if err != nil {
		microbinError(w, http.StatusInternalServerError, "could not create paste", r)
		return
	}
	result := map[string]string{"id": id, "url": c.ph.PasteURL(r, id), "deleteToken": deleteToken}
	if wantsMicrobinJSON(r) {
		writeJSON(w, http.StatusCreated, result)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprintln(w, result["url"])
}

func microbinFieldsFromForm(r *http.Request, body *microbinCreateFields) {
	body.Content = r.FormValue("content")
	body.Title = r.FormValue("title")
	body.Language = r.FormValue("language")
	body.Visibility = r.FormValue("visibility")
	body.Expiry = r.FormValue("expiry")
	if n, err := strconv.Atoi(r.FormValue("burn_after")); err == nil {
		body.BurnAfter = n
	}
}

func wantsMicrobinJSON(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json")
}

func microbinError(w http.ResponseWriter, status int, message string, r *http.Request) {
	if wantsMicrobinJSON(r) {
		writeJSON(w, status, map[string]string{"error": message})
		return
	}
	http.Error(w, message, status)
}

// MicrobinGet returns the target's bare JSON record or plain paste content.
func (c *Handler) MicrobinGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	paste, err := c.db.GetPasteByID(id)
	if err != nil || paste == nil || (paste.ExpiresAt != nil && paste.ExpiresAt.Before(time.Now())) {
		microbinError(w, http.StatusNotFound, "paste not found", r)
		return
	}
	views, burned, err := c.db.IncrementViewsAndCheckBurn(id)
	if err == nil {
		paste.Views = views
	} else {
		paste.Views++
	}
	if burned {
		c.ph.InvalidatePasteCache(id)
		metric.PastesDeletedTotal.Inc()
	}
	if wantsMicrobinJSON(r) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"id": paste.ID, "title": paste.Title, "content": paste.Content,
			"content_type": paste.ContentType,
			"language":     paste.Language, "visibility": visibilityName(paste.Visibility),
			"burn_after": paste.BurnAfter, "views": paste.Views,
			"created_at": paste.CreatedAt, "expires_at": paste.ExpiresAt,
		})
		return
	}
	content := []byte(paste.Content)
	contentType := paste.ContentType
	if contentType != "" {
		if !strings.HasPrefix(contentType, "text/") {
			if decoded, err := base64.StdEncoding.DecodeString(paste.Content); err == nil {
				content = decoded
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if isActiveMicrobinContent(contentType) {
			filename := strings.Map(func(r rune) rune {
				if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == '/' {
					return '_'
				}
				return r
			}, paste.Title)
			if filename == "" {
				filename = paste.ID
			}
			w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"; filename*=UTF-8''`+url.PathEscape(filename))
		}
	} else if contentType == "" {
		contentType = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(content)
}

func isActiveMicrobinContent(contentType string) bool {
	baseType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		baseType = contentType
	}
	switch strings.ToLower(baseType) {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "text/xml", "application/xml":
		return true
	default:
		return false
	}
}

func visibilityName(visibility int) string {
	if visibility == model.VisibilityUnlisted {
		return "unlisted"
	}
	return "public"
}

// MicrobinDelete uses the compatibility deleteToken, never the native owner
// token/cookie mechanism.
func (c *Handler) MicrobinDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	token := r.URL.Query().Get("deleteToken")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			if err := c.parseFormLimited(w, r); err == nil {
				token = r.FormValue("deleteToken")
			}
		}
	}
	if token == "" {
		microbinError(w, http.StatusBadRequest, "deleteToken is required", r)
		return
	}
	if err := c.db.DeletePasteByToken(id, handler.HashToken(token)); err != nil {
		microbinError(w, http.StatusNotFound, "paste not found or invalid token", r)
		return
	}
	c.ph.InvalidatePasteCache(id)
	metric.PastesDeletedTotal.Inc()
	if wantsMicrobinJSON(r) {
		writeJSON(w, http.StatusOK, map[string]string{"message": "paste deleted"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintln(w, "Paste Removed")
}

// MicrobinList returns public pastes using the compat JSON/text formats.
func (c *Handler) MicrobinList(w http.ResponseWriter, r *http.Request) {
	pastes, _, err := c.db.GetPublicPastes(1, 100)
	if err != nil {
		microbinError(w, http.StatusInternalServerError, "could not list pastes", r)
		return
	}
	if wantsMicrobinJSON(r) {
		items := make([]map[string]interface{}, 0, len(pastes))
		for _, paste := range pastes {
			items = append(items, map[string]interface{}{"id": paste.ID, "title": paste.Title, "language": paste.Language, "views": paste.Views, "created_at": paste.CreatedAt})
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, paste := range pastes {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", paste.ID, c.ph.PasteURL(r, paste.ID), paste.Title)
	}
}
