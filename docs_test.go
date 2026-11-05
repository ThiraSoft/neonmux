package main

import (
	"os"
	"strings"
	"testing"
)

// TestDocumentationKittyGraphics verifies that documentation across README,
// docs/architecture.md, skills/neonmux/SKILL.md and vt/doc.go accurately
// reflects the kitty graphics support and remaining limitations.
func TestDocumentationKittyGraphics(t *testing.T) {
	// 1. README.md
	readmeBytes, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	readme := string(readmeBytes)

	if !strings.Contains(readme, "**Images in your terminals.**") {
		t.Error("README.md missing '**Images in your terminals.**'")
	}
	if strings.Contains(readme, "No graphics protocol (sixel, kitty): images go through `neonmux open`.") {
		t.Error("README.md still contains old 'No graphics protocol' limitation")
	}
	if strings.Contains(readme, "docs/kitty.gif") {
		t.Error("README.md should not reference docs/kitty.gif yet")
	}
	if !strings.Contains(readme, "no animation") || !strings.Contains(readme, "scroll regions do not") {
		t.Error("README.md limitations missing graphics limits (animation, scroll regions)")
	}

	// 2. skills/neonmux/SKILL.md
	skillBytes, err := os.ReadFile("skills/neonmux/SKILL.md")
	if err != nil {
		t.Fatalf("reading skills/neonmux/SKILL.md: %v", err)
	}
	skill := string(skillBytes)

	if !strings.Contains(skill, "Show the user an image") {
		t.Error("skills/neonmux/SKILL.md missing 'Show the user an image' section")
	}
	if !strings.Contains(skill, "neonmux open") {
		t.Error("skills/neonmux/SKILL.md missing 'neonmux open'")
	}
	if !strings.Contains(skill, "kitten icat") {
		t.Error("skills/neonmux/SKILL.md missing mention of kitten icat")
	}

	// 3. docs/architecture.md
	archBytes, err := os.ReadFile("docs/architecture.md")
	if err != nil {
		t.Fatalf("reading docs/architecture.md: %v", err)
	}
	arch := string(archBytes)

	for _, want := range []string{
		"kitty graphics protocol",
		"snacks.nvim",
		"image.nvim",
		"unicode placeholders",
		"phone",
		"no sixel",
		"no animation",
	} {
		if !strings.Contains(strings.ToLower(arch), want) {
			t.Errorf("docs/architecture.md missing %q", want)
		}
	}

	// 4. vt/doc.go
	vtDocBytes, err := os.ReadFile("vt/doc.go")
	if err != nil {
		t.Fatalf("reading vt/doc.go: %v", err)
	}
	vtDoc := string(vtDocBytes)

	for _, want := range []string{
		"Cell.UL",
		"MakeCellUL",
		"SGR 58",
		"SGR 59",
		"16 bytes",
	} {
		if !strings.Contains(vtDoc, want) {
			t.Errorf("vt/doc.go missing %q", want)
		}
	}
}
