package builtin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"router-go/internal/provider"
)

// Every built-in provider ships a logo (web/public/providers/<id>.png) and is listed in LOGOS in Builtin.tsx;
// without both the dashboard falls back to two-letter initials.
func TestEveryProviderHasAnIcon(t *testing.T) {
	src, err := os.ReadFile("../../../web/src/components/providers/Builtin.tsx")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`LOGOS = new Set\(\[([^\]]*)\]`).FindSubmatch(src)
	if m == nil {
		t.Fatal("LOGOS set not found in Builtin.tsx")
	}
	listed := string(m[1])
	for _, p := range provider.All() {
		png, err := os.ReadFile(filepath.Join("../../../web/public/providers", p.ID+".png"))
		if err != nil || !strings.HasPrefix(string(png), "\x89PNG") {
			t.Errorf("provider %q: web/public/providers/%s.png is missing or not a PNG", p.ID, p.ID)
		}
		if !strings.Contains(listed, `"`+p.ID+`"`) {
			t.Errorf("provider %q: add it to LOGOS in web/src/components/providers/Builtin.tsx", p.ID)
		}
	}
}
