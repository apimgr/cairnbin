package terminal_test

import (
	"testing"

	"github.com/apimgr/cairnbin/src/common/terminal"
)

// TestNewSymbolSet_Dumb verifies ASCII fallback when dumb=true.
func TestNewSymbolSet_Dumb(t *testing.T) {
	s := terminal.NewSymbolSet(true)
	if s.TopLeft != "+" {
		t.Errorf("TopLeft: got %q, want %q", s.TopLeft, "+")
	}
	if s.CheckMark != "[ok]" {
		t.Errorf("CheckMark: got %q, want %q", s.CheckMark, "[ok]")
	}
	if s.Ellipsis != "..." {
		t.Errorf("Ellipsis: got %q, want %q", s.Ellipsis, "...")
	}
}

// TestNewSymbolSet_Unicode verifies Unicode symbols when dumb=false.
func TestNewSymbolSet_Unicode(t *testing.T) {
	s := terminal.NewSymbolSet(false)
	if s.TopLeft != "┌" {
		t.Errorf("TopLeft: got %q, want %q", s.TopLeft, "┌")
	}
	if s.CheckMark != "✓" {
		t.Errorf("CheckMark: got %q, want %q", s.CheckMark, "✓")
	}
	if s.Ellipsis != "…" {
		t.Errorf("Ellipsis: got %q, want %q", s.Ellipsis, "…")
	}
}

// TestSymbolSet_AllFieldsPopulated verifies all SymbolSet fields are non-empty.
func TestSymbolSet_AllFieldsPopulated(t *testing.T) {
	cases := []struct {
		name string
		dumb bool
	}{
		{"unicode", false},
		{"ascii", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := terminal.NewSymbolSet(tc.dumb)
			fields := []struct {
				name  string
				value string
			}{
				{"TopLeft", s.TopLeft},
				{"TopRight", s.TopRight},
				{"BottomLeft", s.BottomLeft},
				{"BottomRight", s.BottomRight},
				{"Horizontal", s.Horizontal},
				{"Vertical", s.Vertical},
				{"TLeft", s.TLeft},
				{"TRight", s.TRight},
				{"TTop", s.TTop},
				{"TBottom", s.TBottom},
				{"Cross", s.Cross},
				{"ArrowRight", s.ArrowRight},
				{"ArrowLeft", s.ArrowLeft},
				{"ArrowUp", s.ArrowUp},
				{"ArrowDown", s.ArrowDown},
				{"CheckMark", s.CheckMark},
				{"Cross2", s.Cross2},
				{"Bullet", s.Bullet},
				{"Ellipsis", s.Ellipsis},
				{"TreeBranch", s.TreeBranch},
				{"TreeLast", s.TreeLast},
				{"TreePipe", s.TreePipe},
			}
			for _, f := range fields {
				if f.value == "" {
					t.Errorf("%s is empty", f.name)
				}
			}
		})
	}
}
