package tokenharbor

import (
	"testing"

	"router-go/internal/provider"
)

func TestRegistered(t *testing.T) {
	p, ok := provider.ByAlias("th")
	if !ok || p.ID != "tokenharbor" || p.Category != provider.CategoryAPIKey || !p.Routable() || p.NoAuth {
		t.Fatalf("tokenharbor not registered as a routable apikey provider: %+v", p)
	}
	if p.BaseURL != "https://tokenharbor.ai/v1" || !p.HasModel("some-new-model") || !p.HasModel("deepseek-v4.1-flash:free") {
		t.Fatalf("bad base URL or model acceptance: %+v", p)
	}
}
