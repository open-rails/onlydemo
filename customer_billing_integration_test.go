package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/open-rails/openrails"
)

// Exercise the library's native customer routes using the same real AuthKit
// tokens and settled purchase as the blog workflow, without an app HTTP facade.
func assertCustomerBillingSurface(t *testing.T, app *blogTestServer, buyerID, buyerToken, outsiderToken, productID string) {
	t.Helper()
	const base = "/billing/v1/me"
	blogTestRequest(t, app, http.MethodGet, base+"/products", "", nil, http.StatusUnauthorized)
	raw := blogTestRequest(t, app, http.MethodGet, base+"/products?limit=1", buyerToken, nil, http.StatusOK)
	var products openrails.ProductAccessList
	if err := json.Unmarshal(raw, &products); err != nil || len(products.Data) != 1 || products.Data[0].ProductID != productID || products.Data[0].ProductName == "" {
		t.Fatalf("customer purchased-product page: error=%v body=%s", err, raw)
	}
	raw = blogTestRequest(t, app, http.MethodGet, base+"/products?customer_id="+buyerID, outsiderToken, nil, http.StatusOK,
		http.Header{"X-User-ID": {buyerID}})
	if err := json.Unmarshal(raw, &products); err != nil || len(products.Data) != 0 {
		t.Fatalf("customer override leaked another buyer's purchases: error=%v body=%s", err, raw)
	}
	var payments struct {
		Data []struct {
			CustomerID string          `json:"customer_id"`
			Amount     string          `json:"amount"`
			CreatedAt  time.Time       `json:"created_at"`
			Price      json.RawMessage `json:"price"`
		} `json:"data"`
	}
	raw = blogTestRequest(t, app, http.MethodGet, base+"/payments?limit=1", buyerToken, nil, http.StatusOK)
	if err := json.Unmarshal(raw, &payments); err != nil || len(payments.Data) != 1 || payments.Data[0].CustomerID != buyerID || payments.Data[0].Amount != "4990000" || payments.Data[0].CreatedAt.IsZero() || len(payments.Data[0].Price) == 0 {
		t.Fatalf("customer payment history lacks the settled purchase: error=%v body=%s", err, raw)
	}
	raw = blogTestRequest(t, app, http.MethodGet, base+"/payments?customer_id="+buyerID, outsiderToken, nil, http.StatusOK)
	if err := json.Unmarshal(raw, &payments); err != nil || len(payments.Data) != 0 {
		t.Fatalf("payment history crossed customers: error=%v body=%s", err, raw)
	}
	for _, path := range []string{"/invoices?limit=1", "/subscriptions?limit=1", "/payment-methods"} {
		blogTestRequest(t, app, http.MethodGet, base+path, buyerToken, nil, http.StatusOK)
	}
	for _, path := range []string{"/checkout", "/billing-portal", "/subscriptions/sub_00000000-0000-4000-8000-000000000001/change-tier"} {
		blogTestRequest(t, app, http.MethodPost, base+path, buyerToken, map[string]any{}, http.StatusNotFound)
	}
	raw = blogTestRequest(t, app, http.MethodGet, "/billing/v1/capabilities", "", nil, http.StatusOK)
	var capabilities struct {
		RouteGroups map[string]bool `json:"route_groups"`
		Features    map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(raw, &capabilities); err != nil || !capabilities.RouteGroups["customer"] || capabilities.RouteGroups["checkout"] || capabilities.Features["stripe_billing_portal"] || capabilities.Features["provider_credential_writes"] {
		t.Fatalf("customer capabilities do not match mounted routes: error=%v body=%s", err, raw)
	}
}
