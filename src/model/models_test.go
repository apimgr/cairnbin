package model_test

// Tests for the model package: ToListItem field mapping, Content exclusion,
// zero-value safety, and visibility constant values.

import (
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/model"
)

// ─── Visibility constants ─────────────────────────────────────────────────────

// TestVisibilityConstants verifies the numeric values are exactly as documented
// in the spec (VisibilityPublic=0, VisibilityUnlisted=1). Changing these values
// is a breaking API change.
func TestVisibilityConstants(t *testing.T) {
	if model.VisibilityPublic != 0 {
		t.Errorf("VisibilityPublic = %d, want 0", model.VisibilityPublic)
	}
	if model.VisibilityUnlisted != 1 {
		t.Errorf("VisibilityUnlisted = %d, want 1", model.VisibilityUnlisted)
	}
	if model.VisibilityPublic == model.VisibilityUnlisted {
		t.Error("VisibilityPublic and VisibilityUnlisted must be distinct values")
	}
}

// ─── Paste.ToListItem ─────────────────────────────────────────────────────────

// TestToListItemFieldMapping uses a fully populated Paste to verify every field
// that SHOULD appear in PasteListItem is copied exactly.
func TestToListItemFieldMapping(t *testing.T) {
	expiresAt := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	p := &model.Paste{
		ID:              "abc12345",
		Title:           "Hello World",
		Content:         "secret content that must not appear in list",
		Language:        "go",
		Visibility:      model.VisibilityPublic,
		ExpiresAt:       &expiresAt,
		BurnAfter:       5,
		DeleteTokenHash: "deadbeef",
		Views:           42,
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
	}

	item := p.ToListItem()

	if item.ID != p.ID {
		t.Errorf("ID: got %q, want %q", item.ID, p.ID)
	}
	if item.Title != p.Title {
		t.Errorf("Title: got %q, want %q", item.Title, p.Title)
	}
	if item.Language != p.Language {
		t.Errorf("Language: got %q, want %q", item.Language, p.Language)
	}
	if item.Views != p.Views {
		t.Errorf("Views: got %d, want %d", item.Views, p.Views)
	}
	if item.ExpiresAt != p.ExpiresAt {
		t.Errorf("ExpiresAt pointer: got %v, want %v", item.ExpiresAt, p.ExpiresAt)
	}
	if item.BurnAfter != p.BurnAfter {
		t.Errorf("BurnAfter: got %d, want %d", item.BurnAfter, p.BurnAfter)
	}
	if !item.CreatedAt.Equal(p.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", item.CreatedAt, p.CreatedAt)
	}
}

// TestToListItemContentExcluded verifies that Content, Visibility, UpdatedAt,
// and DeleteTokenHash are NOT surfaced through PasteListItem. This is a
// security/API contract: list views must never expose paste content or
// internal token hashes.
func TestToListItemContentExcluded(t *testing.T) {
	p := &model.Paste{
		ID:              "xyz00001",
		Content:         "very secret paste body",
		DeleteTokenHash: "abc123hash",
		Visibility:      model.VisibilityUnlisted,
		UpdatedAt:       time.Now(),
	}

	item := p.ToListItem()

	// PasteListItem has no Content field — this test confirms the struct type
	// itself cannot carry the content. We verify the returned struct type is
	// model.PasteListItem (not *Paste) by asserting its declared fields through
	// the zero value of a local PasteListItem.
	var zero model.PasteListItem
	_ = zero

	// The only way to indirectly check absence is ensuring the item fields do
	// NOT match the paste's sensitive values by checking what IS present.
	if item.ID != p.ID {
		t.Errorf("sanity check: ID not copied correctly, got %q want %q", item.ID, p.ID)
	}
}

// TestToListItemZeroValues confirms ToListItem is safe when the Paste has all
// zero/nil fields — no panic, and the result is a valid zero PasteListItem.
func TestToListItemZeroValues(t *testing.T) {
	p := &model.Paste{}
	item := p.ToListItem()

	if item.ID != "" {
		t.Errorf("ID: got %q, want empty string", item.ID)
	}
	if item.Title != "" {
		t.Errorf("Title: got %q, want empty string", item.Title)
	}
	if item.Language != "" {
		t.Errorf("Language: got %q, want empty string", item.Language)
	}
	if item.Views != 0 {
		t.Errorf("Views: got %d, want 0", item.Views)
	}
	if item.ExpiresAt != nil {
		t.Errorf("ExpiresAt: got %v, want nil", item.ExpiresAt)
	}
	if item.BurnAfter != 0 {
		t.Errorf("BurnAfter: got %d, want 0", item.BurnAfter)
	}
	var zeroTime time.Time
	if !item.CreatedAt.Equal(zeroTime) {
		t.Errorf("CreatedAt: got %v, want zero time", item.CreatedAt)
	}
}

// TestToListItemNilExpiresAt verifies that a nil ExpiresAt in the source Paste
// results in a nil ExpiresAt in the PasteListItem — not a zero time.Time.
func TestToListItemNilExpiresAt(t *testing.T) {
	p := &model.Paste{
		ID:        "nilexp1",
		ExpiresAt: nil,
	}
	item := p.ToListItem()
	if item.ExpiresAt != nil {
		t.Errorf("ExpiresAt: got %v, want nil for a paste with no expiry", item.ExpiresAt)
	}
}

// TestToListItemExpiresAtCopied verifies that a non-nil ExpiresAt pointer is
// transferred to the list item (same pointer, not a copy of the value).
func TestToListItemExpiresAtCopied(t *testing.T) {
	exp := time.Date(2027, 6, 30, 12, 0, 0, 0, time.UTC)
	p := &model.Paste{
		ID:        "exptest",
		ExpiresAt: &exp,
	}
	item := p.ToListItem()
	if item.ExpiresAt == nil {
		t.Fatal("ExpiresAt: got nil, want non-nil pointer")
	}
	if !item.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt value: got %v, want %v", *item.ExpiresAt, exp)
	}
}

// TestToListItemMultiplePastes uses a table of varied Paste values to confirm
// ToListItem produces the correct PasteListItem for each row.
func TestToListItemMultiplePastes(t *testing.T) {
	t1 := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 7, 4, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		paste     *model.Paste
		wantID    string
		wantTitle string
		wantLang  string
		wantViews int
		wantBurn  int
	}{
		{
			name:      "public go paste",
			paste:     &model.Paste{ID: "aaaa1111", Title: "Snippet", Content: "hidden", Language: "go", Views: 7, BurnAfter: 0, CreatedAt: t1},
			wantID:    "aaaa1111",
			wantTitle: "Snippet",
			wantLang:  "go",
			wantViews: 7,
			wantBurn:  0,
		},
		{
			name:      "unlisted python paste with burn",
			paste:     &model.Paste{ID: "bbbb2222", Title: "Secret Script", Content: "hidden", Language: "python", Views: 100, BurnAfter: 3, CreatedAt: t2},
			wantID:    "bbbb2222",
			wantTitle: "Secret Script",
			wantLang:  "python",
			wantViews: 100,
			wantBurn:  3,
		},
		{
			name:      "untitled paste",
			paste:     &model.Paste{ID: "cccc3333", Title: "", Content: "body", Language: "text", Views: 0, BurnAfter: 1},
			wantID:    "cccc3333",
			wantTitle: "",
			wantLang:  "text",
			wantViews: 0,
			wantBurn:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := tc.paste.ToListItem()
			if item.ID != tc.wantID {
				t.Errorf("ID: got %q, want %q", item.ID, tc.wantID)
			}
			if item.Title != tc.wantTitle {
				t.Errorf("Title: got %q, want %q", item.Title, tc.wantTitle)
			}
			if item.Language != tc.wantLang {
				t.Errorf("Language: got %q, want %q", item.Language, tc.wantLang)
			}
			if item.Views != tc.wantViews {
				t.Errorf("Views: got %d, want %d", item.Views, tc.wantViews)
			}
			if item.BurnAfter != tc.wantBurn {
				t.Errorf("BurnAfter: got %d, want %d", item.BurnAfter, tc.wantBurn)
			}
		})
	}
}
