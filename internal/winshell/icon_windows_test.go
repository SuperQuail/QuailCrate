package winshell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractIconFromWindowsSystemFileAndFallbackExtension(t *testing.T) {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	notepad := filepath.Join(systemRoot, "notepad.exe")
	img := ExtractIcon(notepad, 48)
	if img == nil || img.Rect.Empty() {
		t.Fatalf("expected non-nil icon for %s", notepad)
	}
	nonZero := 0
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0 {
			nonZero++
		}
	}
	if nonZero < 50 {
		t.Fatalf("extracted icon appears empty (%d opaque pixels)", nonZero)
	}

	// Non-existent path with extension should still return a shell file-type icon.
	fallback := ExtractIcon(`C:\nonexistent-quailcrate-path\report.txt`, 48)
	if fallback == nil || fallback.Rect.Empty() {
		t.Fatal("expected fallback shell icon for .txt extension")
	}
}
