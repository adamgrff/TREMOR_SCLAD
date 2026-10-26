package assemblyorders

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestOzonOptionalConfigurationAndNoTransport(t *testing.T) {
	t.Setenv("OZON_CLIENT_ID", "")
	t.Setenv("OZON_API_KEY", "")
	if LoadOzonConfig().Configured() {
		t.Fatal("missing credentials counted as configured")
	}
	t.Setenv("OZON_CLIENT_ID", "test-client-value")
	t.Setenv("OZON_API_KEY", "test-secret-value")
	config := LoadOzonConfig()
	if !config.Configured() {
		t.Fatal("configuration not loaded")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, config), "test-secret-value") {
			t.Fatal("configuration exposes key")
		}
	}
	if _, err := (UnavailableOzonClient{}).FetchAssemblyOrders(context.Background()); err == nil {
		t.Fatal("unverified client reported success")
	}
}
func TestOrderBoundaryValidation(t *testing.T) {
	order := Order{Source: "ozon", Number: "REAL", CreatedAt: "2026-10-11T10:00:00Z", Items: []Item{{Name: "Alpha", SKU: "A", Quantity: 2}}}
	if err := order.Validate(); err != nil {
		t.Fatal(err)
	}
	order.Items = append(order.Items, Item{Name: "Again", SKU: "a", Quantity: 1})
	if order.Validate() == nil {
		t.Fatal("duplicate case-insensitive SKU accepted")
	}
	order.Items = order.Items[:1]
	order.Items[0].Quantity = 0
	if order.Validate() == nil {
		t.Fatal("invalid quantity accepted")
	}
}
