package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestIndexIsEmbedded(t *testing.T) {
	b, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		t.Fatalf("index.html is not embedded: %v", err)
	}
	if !strings.Contains(string(b), "<title>uped</title>") {
		t.Errorf("index.html has no <title>uped</title>")
	}
}
