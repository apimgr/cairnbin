package httputil

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// HTML2TextConverter converts rendered HTML to formatted terminal text.
// This is a custom Go function — NOT a library wrapper.
// Used for HTTP tools (curl, wget, httpie) that just dump output.
func HTML2TextConverter(htmlStr string, width int) string {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return stripTags(htmlStr)
	}

	var buf strings.Builder
	convertNode(&buf, doc, width, 0)
	return buf.String()
}

// convertNode recursively converts HTML nodes to formatted text.
func convertNode(buf *strings.Builder, n *html.Node, width, indent int) {
	switch n.Type {
	case html.ElementNode:
		switch n.Data {
		case "h1":
			text := getTextContent(n)
			line := strings.Repeat("═", width)
			buf.WriteString(line + "\n")
			buf.WriteString(centerText(strings.ToUpper(text), width) + "\n")
			buf.WriteString(line + "\n\n")
		case "h2":
			text := getTextContent(n)
			buf.WriteString("─── " + text + " ───\n\n")
		case "h3":
			text := getTextContent(n)
			buf.WriteString("► " + text + "\n\n")
		case "p":
			text := getTextContent(n)
			buf.WriteString(wordWrap(text, width-indent) + "\n\n")
		case "ul":
			convertList(buf, n, width, indent, false)
		case "ol":
			convertList(buf, n, width, indent, true)
		case "a":
			text := getTextContent(n)
			href := getAttr(n, "href")
			buf.WriteString(text + " [" + href + "]")
		case "strong", "b":
			buf.WriteString("*" + getTextContent(n) + "*")
		case "em", "i":
			buf.WriteString("_" + getTextContent(n) + "_")
		case "code":
			buf.WriteString("`" + getTextContent(n) + "`")
		case "pre":
			text := getTextContent(n)
			for _, line := range strings.Split(text, "\n") {
				buf.WriteString("    " + line + "\n")
			}
			buf.WriteString("\n")
		case "table":
			convertTable(buf, n, width)
		case "hr":
			buf.WriteString(strings.Repeat("─", width) + "\n\n")
		case "blockquote":
			text := getTextContent(n)
			for _, line := range strings.Split(text, "\n") {
				buf.WriteString("│ " + line + "\n")
			}
			buf.WriteString("\n")
		case "br":
			buf.WriteString("\n")
		case "script", "style", "form", "input", "button", "select", "textarea":
			// Skip non-textual and non-interactive elements entirely.
		default:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				convertNode(buf, c, width, indent)
			}
		}
	case html.TextNode:
		text := strings.TrimSpace(n.Data)
		if text != "" {
			buf.WriteString(text)
		}
	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			convertNode(buf, c, width, indent)
		}
	}
}

// convertList renders <ul> or <ol> nodes as text bullet/numbered lists.
func convertList(buf *strings.Builder, n *html.Node, width, indent int, ordered bool) {
	count := 1
	pad := strings.Repeat(" ", indent)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "li" {
			continue
		}
		text := getTextContent(c)
		if ordered {
			fmt.Fprintf(buf, "%s  %d. %s\n", pad, count, text)
			count++
		} else {
			buf.WriteString(pad + "  • " + text + "\n")
		}
		_ = width
	}
	buf.WriteString("\n")
}

// convertTable renders an HTML <table> as an ASCII box table.
func convertTable(buf *strings.Builder, n *html.Node, width int) {
	// Collect rows and cells.
	var rows [][]string
	var walkTable func(*html.Node)
	walkTable = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			var cells []string
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") {
					cells = append(cells, getTextContent(c))
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkTable(c)
		}
	}
	walkTable(n)

	if len(rows) == 0 {
		return
	}

	// Determine column count and widths.
	cols := 0
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	colWidths := make([]int, cols)
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > colWidths[i] {
				colWidths[i] = len(cell)
			}
		}
	}

	_ = width

	buildBorder := func(left, mid, right, fill string) string {
		var s strings.Builder
		s.WriteString(left)
		for i, w := range colWidths {
			s.WriteString(strings.Repeat(fill, w+2))
			if i < len(colWidths)-1 {
				s.WriteString(mid)
			}
		}
		s.WriteString(right)
		return s.String()
	}

	top := buildBorder("┌", "┬", "┐", "─")
	sep := buildBorder("├", "┼", "┤", "─")
	bot := buildBorder("└", "┴", "┘", "─")

	buf.WriteString(top + "\n")
	for rowIdx, row := range rows {
		var line strings.Builder
		line.WriteString("│")
		for i := 0; i < cols; i++ {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			line.WriteString(" " + cell + strings.Repeat(" ", colWidths[i]-len(cell)+1) + "│")
		}
		buf.WriteString(line.String() + "\n")
		if rowIdx == 0 && len(rows) > 1 {
			buf.WriteString(sep + "\n")
		}
	}
	buf.WriteString(bot + "\n\n")
}

// getTextContent returns the concatenated text content of a node and all its descendants.
func getTextContent(n *html.Node) string {
	var buf strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			buf.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(buf.String())
}

// getAttr returns the value of the named attribute on an element node, or "".
func getAttr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// centerText pads text with spaces to center it within width columns.
func centerText(text string, width int) string {
	if len(text) >= width {
		return text
	}
	padding := (width - len(text)) / 2
	return strings.Repeat(" ", padding) + text
}

// wordWrap wraps text to the given column width, breaking on word boundaries.
func wordWrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}

	var lines []string
	current := words[0]
	for _, word := range words[1:] {
		if len(current)+1+len(word) <= width {
			current += " " + word
		} else {
			lines = append(lines, current)
			current = word
		}
	}
	lines = append(lines, current)
	return strings.Join(lines, "\n")
}

// reTags matches HTML tags for the fallback strip operation.
var reTags = regexp.MustCompile(`<[^>]+>`)

// stripTags removes all HTML tags from s, used as a fallback when HTML parsing fails.
func stripTags(s string) string {
	return reTags.ReplaceAllString(s, "")
}
