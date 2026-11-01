package model

import (
	"time"
)

// Visibility constants for Paste
const (
	// listed in /recent, accessible to all
	VisibilityPublic = 0
	// accessible via direct link only
	VisibilityUnlisted = 1
)

// Paste represents a stored paste/snippet.
type Paste struct {
	ID      string `json:"id"`
	OwnerID string `json:"-"`
	Title   string `json:"title"`
	// omitted in list views; redirect target URL when IsLink
	Content string `json:"content,omitempty"`
	// detected MIME type; empty = plain text
	ContentType string `json:"content_type,omitempty"`
	Language    string `json:"language"`
	// 0=public, 1=unlisted
	Visibility int `json:"visibility"`
	// true = content is a redirect target (http/https only); GET /{id} issues 302 instead of rendering
	IsLink    bool       `json:"is_link"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// 0=disabled, 1-9999 views then delete
	BurnAfter int `json:"burn_after"`
	// SHA-256(delete_token), never in JSON
	DeleteTokenHash string    `json:"-"`
	Views           int       `json:"views"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// PasteListItem is a minimal paste for list and recent views.
type PasteListItem struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Language  string     `json:"language"`
	Views     int        `json:"views"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	BurnAfter int        `json:"burn_after"`
	CreatedAt time.Time  `json:"created_at"`
}

// CreateResponse includes a plaintext owner token only when creation issued a new owner identity.
type CreateResponse struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Language   string     `json:"language"`
	Visibility int        `json:"visibility"`
	IsLink     bool       `json:"is_link"`
	BurnAfter  int        `json:"burn_after"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	Views      int        `json:"views"`
	CreatedAt  time.Time  `json:"created_at"`
	Link       string     `json:"link"`
	// Plaintext tok_... returned only when a new owner identity is issued.
	OwnerToken string `json:"owner_token,omitempty"`
}

// ToListItem converts a Paste to its minimal list representation.
func (p *Paste) ToListItem() PasteListItem {
	return PasteListItem{
		ID:        p.ID,
		Title:     p.Title,
		Language:  p.Language,
		Views:     p.Views,
		ExpiresAt: p.ExpiresAt,
		BurnAfter: p.BurnAfter,
		CreatedAt: p.CreatedAt,
	}
}
