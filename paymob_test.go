package paymob

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/togo-framework/payment"
)

// mockPaymob serves the 3-step flow (auth → order → payment_key).
func mockPaymob(t *testing.T) (*provider, *httptest.Server) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/tokens":
			json.NewEncoder(w).Encode(map[string]any{"token": "auth_tok"})
		case "/api/ecommerce/orders":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if b["auth_token"] != "auth_tok" {
				t.Errorf("order missing auth_token: %v", b)
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 555})
		case "/api/acceptance/payment_keys":
			json.NewEncoder(w).Encode(map[string]any{"token": "pay_tok"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	return &provider{apiKey: "key", integrationID: "42", iframeID: "9", hmac: "H", base: srv.URL, hc: srv.Client()}, srv
}

func TestCheckoutFlow(t *testing.T) {
	p, srv := mockPaymob(t)
	defer srv.Close()
	cs, err := p.CreateCheckoutSession(context.Background(), payment.CheckoutRequest{
		Amount: payment.Money{Amount: 5000, Currency: "EGP"}, Customer: payment.Customer{Name: "Ahmed Ali", Email: "a@b.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cs.ID != "555" {
		t.Errorf("order id = %q", cs.ID)
	}
	if cs.URL != srv.URL+"/api/acceptance/iframes/9?payment_token=pay_tok" {
		t.Errorf("iframe url = %q", cs.URL)
	}
}

func TestChargeWithoutTokenIsPending(t *testing.T) {
	p, srv := mockPaymob(t)
	defer srv.Close()
	ch, err := p.CreateCharge(context.Background(), payment.ChargeRequest{Amount: payment.Money{Amount: 100, Currency: "EGP"}})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Status != "pending" || ch.ID != "555" {
		t.Errorf("got %+v", ch)
	}
}

func TestHMACVerification(t *testing.T) {
	p := &provider{hmac: "H"}
	obj := map[string]any{
		"amount_cents": float64(5000), "created_at": "2026-01-01", "currency": "EGP",
		"error_occured": false, "has_parent_transaction": false, "id": float64(777),
		"integration_id": float64(42), "is_3d_secure": false, "is_auth": false, "is_capture": false,
		"is_refunded": false, "is_standalone_payment": true, "is_voided": false,
		"order": map[string]any{"id": float64(555)}, "owner": float64(1), "pending": false,
		"source_data": map[string]any{"pan": "1234", "sub_type": "MasterCard", "type": "card"},
		"success":     true,
	}
	good := p.computeHMAC(obj)
	body, _ := json.Marshal(map[string]any{"type": "TRANSACTION", "obj": obj, "hmac": good})
	ev, err := p.HandleWebhook(context.Background(), nil, body)
	if err != nil {
		t.Fatalf("valid HMAC rejected: %v", err)
	}
	if ev.Type != "transaction.succeeded" || ev.ID != "777" {
		t.Errorf("got %+v", ev)
	}
	bad, _ := json.Marshal(map[string]any{"type": "TRANSACTION", "obj": obj, "hmac": "deadbeef"})
	if _, err := p.HandleWebhook(context.Background(), nil, bad); err == nil {
		t.Error("bad HMAC accepted")
	}
}
