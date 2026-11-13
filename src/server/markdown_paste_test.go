package server

import (
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/model"
)

func TestRenderPasteMarkdownSanitizesUserContent(t *testing.T) {
	got := string(renderPasteMarkdown("# Heading\n\n<script>alert(1)</script>\n\n**safe**"))
	if !strings.Contains(got, "<h1>Heading</h1>") || !strings.Contains(got, "<strong>safe</strong>") {
		t.Fatalf("markdown preview missing expected formatting: %s", got)
	}
	if strings.Contains(got, "<script>") {
		t.Fatalf("markdown preview retained executable HTML: %s", got)
	}
}

func TestClassifyPasteContentLabelsSupportedFormats(t *testing.T) {
	tests := []struct {
		name, title, language, contentType, wantLabel string
		wantMarkdown, wantImage                       bool
	}{
		{"docx", "report.docx", "text", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "Word document (DOCX)", false, false},
		{"markdown", "notes.md", "markdown", "", "Markdown", true, false},
		{"image", "photo.png", "text", "image/png", "PNG image", false, true},
		{"text", "notes.txt", "text", "", "Text", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyPasteContent(&model.Paste{Title: tt.title, Language: tt.language, ContentType: tt.contentType})
			if got.ContentLabel != tt.wantLabel || got.IsMarkdown != tt.wantMarkdown || got.IsImage != tt.wantImage {
				t.Errorf("classification = {label:%q markdown:%t image:%t}, want {label:%q markdown:%t image:%t}", got.ContentLabel, got.IsMarkdown, got.IsImage, tt.wantLabel, tt.wantMarkdown, tt.wantImage)
			}
		})
	}
}
