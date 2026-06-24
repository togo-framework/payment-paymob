// Package paymob is a Paymob (Egypt) driver for togo payment. Set
// PAYMENT_DRIVER=paymob + PAYMOB_API_KEY + PAYMOB_INTEGRATION_ID + PAYMOB_HMAC.
// Optional PAYMOB_IFRAME_ID (for the hosted iframe URL) and PAYMOB_BASE_URL
// (default: https://accept.paymob.com).
//
// Checkout uses Paymob's 3-step flow: auth → register order → payment key →
// iframe URL. Webhooks are verified with HMAC-SHA512 over the documented field
// concatenation against PAYMOB_HMAC.
package paymob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/togo-framework/payment"
	"github.com/togo-framework/togo"
)

const defaultAPI = "https://accept.paymob.com"

func init() {
	payment.RegisterDriver("paymob", func(k *togo.Kernel) (payment.PaymentProvider, error) {
		api := os.Getenv("PAYMOB_API_KEY")
		integ := os.Getenv("PAYMOB_INTEGRATION_ID")
		if api == "" || integ == "" {
			return nil, errors.New("payment-paymob: PAYMOB_API_KEY and PAYMOB_INTEGRATION_ID are required")
		}
		base := os.Getenv("PAYMOB_BASE_URL")
		if base == "" {
			base = defaultAPI
		}
		return &provider{
			apiKey: api, integrationID: integ, hmac: os.Getenv("PAYMOB_HMAC"),
			iframeID: os.Getenv("PAYMOB_IFRAME_ID"),
			base:     strings.TrimRight(base, "/"), hc: &http.Client{Timeout: 20 * time.Second},
		}, nil
	})
}

type provider struct {
	apiKey        string
	integrationID string
	hmac          string
	iframeID      string
	base          string
	hc            *http.Client
}

func (p *provider) post(ctx context.Context, path string, payload any) (map[string]any, error) {
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if len(b) > 0 {
		_ = json.Unmarshal(b, &m)
	}
	if resp.StatusCode >= 300 {
		return m, fmt.Errorf("paymob: %s: %s", resp.Status, string(b))
	}
	return m, nil
}

func (p *provider) authToken(ctx context.Context) (string, error) {
	m, err := p.post(ctx, "/api/auth/tokens", map[string]any{"api_key": p.apiKey})
	if err != nil {
		return "", err
	}
	return str(m["token"]), nil
}

func (p *provider) registerOrder(ctx context.Context, auth string, amountCents int64, currency, ref string, items []map[string]any) (string, error) {
	m, err := p.post(ctx, "/api/ecommerce/orders", map[string]any{
		"auth_token": auth, "delivery_needed": false, "amount_cents": amountCents,
		"currency": currency, "merchant_order_id": ref, "items": items,
	})
	if err != nil {
		return "", err
	}
	return str(m["id"]), nil
}

func (p *provider) paymentKey(ctx context.Context, auth string, amountCents int64, currency, orderID string, billing map[string]any) (string, error) {
	m, err := p.post(ctx, "/api/acceptance/payment_keys", map[string]any{
		"auth_token": auth, "amount_cents": amountCents, "expiration": 3600,
		"order_id": orderID, "billing_data": billing, "currency": currency,
		"integration_id": p.integrationID,
	})
	if err != nil {
		return "", err
	}
	return str(m["token"]), nil
}

// CreateCheckoutSession runs the full 3-step flow and returns the iframe URL.
func (p *provider) CreateCheckoutSession(ctx context.Context, r payment.CheckoutRequest) (*payment.CheckoutSession, error) {
	auth, err := p.authToken(ctx)
	if err != nil {
		return nil, err
	}
	cents := total(r)
	ref := metaOr(r.Metadata, "merchant_order_id", "co-"+coRef(r))
	orderID, err := p.registerOrder(ctx, auth, cents, orDefault(r.Amount.Currency, "EGP"), ref, items(r))
	if err != nil {
		return nil, err
	}
	payTok, err := p.paymentKey(ctx, auth, cents, orDefault(r.Amount.Currency, "EGP"), orderID, billing(r.Customer))
	if err != nil {
		return nil, err
	}
	url := ""
	if p.iframeID != "" {
		url = fmt.Sprintf("%s/api/acceptance/iframes/%s?payment_token=%s", p.base, p.iframeID, payTok)
	}
	return &payment.CheckoutSession{ID: orderID, URL: url}, nil
}

// CreateCharge runs the flow; with a saved card Token it pays MOTO, otherwise it
// returns a pending charge whose payment completes via the iframe/checkout.
func (p *provider) CreateCharge(ctx context.Context, r payment.ChargeRequest) (*payment.Charge, error) {
	auth, err := p.authToken(ctx)
	if err != nil {
		return nil, err
	}
	ref := metaOr(r.Metadata, "merchant_order_id", "ch-"+ref8(r))
	orderID, err := p.registerOrder(ctx, auth, r.Amount.Amount, orDefault(r.Amount.Currency, "EGP"), ref, []map[string]any{{"name": orDefault(r.Description, "charge"), "amount_cents": r.Amount.Amount, "quantity": 1}})
	if err != nil {
		return nil, err
	}
	payTok, err := p.paymentKey(ctx, auth, r.Amount.Amount, orDefault(r.Amount.Currency, "EGP"), orderID, billing(r.Customer))
	if err != nil {
		return nil, err
	}
	if r.Token != "" {
		m, err := p.post(ctx, "/api/acceptance/payments/pay", map[string]any{
			"source":        map[string]any{"identifier": r.Token, "subtype": "TOKEN"},
			"payment_token": payTok,
		})
		if err != nil {
			return nil, err
		}
		st := "pending"
		if b, ok := m["success"].(bool); ok && b {
			st = "succeeded"
		} else if _, ok := m["success"]; ok {
			st = "failed"
		}
		return &payment.Charge{ID: str(m["id"]), Status: st, Amount: r.Amount, Provider: "paymob", Raw: m}, nil
	}
	return &payment.Charge{ID: orderID, Status: "pending", Amount: r.Amount, Provider: "paymob", Raw: map[string]any{"order_id": orderID, "payment_token": payTok}}, nil
}

func (p *provider) Refund(ctx context.Context, r payment.RefundRequest) error {
	if r.ChargeID == "" {
		return errors.New("paymob: RefundRequest.ChargeID (the transaction id) is required")
	}
	auth, err := p.authToken(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{"auth_token": auth, "transaction_id": r.ChargeID}
	if r.Amount != nil {
		body["amount_cents"] = r.Amount.Amount
	}
	_, err = p.post(ctx, "/api/acceptance/void_refund/refund", body)
	return err
}

func (p *provider) CreateCustomer(context.Context, payment.Customer) (string, error) {
	return "", errors.New("paymob: customers are passed inline as billing data — no standalone customer API")
}

func (p *provider) CreateSubscription(context.Context, payment.SubscriptionRequest) (*payment.Subscription, error) {
	return nil, errors.New("paymob: native subscriptions are not wired — use the togo subscriptions plugin")
}

// HandleWebhook verifies the Paymob HMAC-SHA512 over the documented transaction
// field order and reports the transaction status.
func (p *provider) HandleWebhook(_ context.Context, headers map[string]string, body []byte) (*payment.WebhookEvent, error) {
	var env struct {
		Type string         `json:"type"`
		Obj  map[string]any `json:"obj"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("paymob: bad webhook body: %w", err)
	}
	o := env.Obj
	if o == nil {
		return nil, errors.New("paymob: webhook missing obj")
	}
	got := header(headers, "hmac")
	if got == "" {
		if hm := topLevelHMAC(body); hm != "" {
			got = hm
		}
	}
	if p.hmac != "" && got != "" {
		want := p.computeHMAC(o)
		if !hmac.Equal([]byte(strings.ToLower(got)), []byte(strings.ToLower(want))) {
			return nil, errors.New("paymob: webhook HMAC mismatch")
		}
	}
	t := "transaction"
	if b, ok := o["success"].(bool); ok {
		if b {
			t = "transaction.succeeded"
		} else {
			t = "transaction.failed"
		}
	}
	return &payment.WebhookEvent{Type: t, ID: str(o["id"]), Provider: "paymob", Raw: o}, nil
}

// computeHMAC concatenates the Paymob transaction fields in the documented order
// and HMAC-SHA512s them with the HMAC secret.
func (p *provider) computeHMAC(o map[string]any) string {
	src, _ := o["source_data"].(map[string]any)
	order, _ := o["order"].(map[string]any)
	fields := []string{
		str(o["amount_cents"]), str(o["created_at"]), str(o["currency"]),
		boolStr(o["error_occured"]), boolStr(o["has_parent_transaction"]),
		str(o["id"]), str(o["integration_id"]), boolStr(o["is_3d_secure"]),
		boolStr(o["is_auth"]), boolStr(o["is_capture"]), boolStr(o["is_refunded"]),
		boolStr(o["is_standalone_payment"]), boolStr(o["is_voided"]),
		str(get(order, "id")), str(o["owner"]), boolStr(o["pending"]),
		str(get(src, "pan")), str(get(src, "sub_type")), str(get(src, "type")),
		boolStr(o["success"]),
	}
	mac := hmac.New(sha512.New, []byte(p.hmac))
	mac.Write([]byte(strings.Join(fields, "")))
	return hex.EncodeToString(mac.Sum(nil))
}

// ── helpers ────────────────────────────────────────────────────────────────

func str(v any) string {
	switch n := v.(type) {
	case string:
		return n
	case float64:
		if n == float64(int64(n)) {
			return strconv.FormatInt(int64(n), 10)
		}
		return strconv.FormatFloat(n, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

func boolStr(v any) string {
	if b, ok := v.(bool); ok {
		if b {
			return "true"
		}
		return "false"
	}
	return str(v)
}

func get(m map[string]any, k string) any {
	if m == nil {
		return nil
	}
	return m[k]
}

func header(h map[string]string, key string) string {
	for k, v := range h {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func topLevelHMAC(body []byte) string {
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	return str(m["hmac"])
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func metaOr(m map[string]string, k, d string) string {
	if v, ok := m[k]; ok && v != "" {
		return v
	}
	return d
}

func billing(c payment.Customer) map[string]any {
	first, last := splitName(c.Name)
	return map[string]any{
		"first_name": first, "last_name": last, "email": orDefault(c.Email, "na@example.com"),
		"phone_number": orDefault(c.Phone, "NA"), "apartment": "NA", "floor": "NA", "street": "NA",
		"building": "NA", "city": "NA", "country": "EG", "state": "NA", "postal_code": "NA",
	}
}

func splitName(n string) (string, string) {
	n = strings.TrimSpace(n)
	if n == "" {
		return "NA", "NA"
	}
	parts := strings.Fields(n)
	if len(parts) == 1 {
		return parts[0], parts[0]
	}
	return parts[0], strings.Join(parts[1:], " ")
}

func total(r payment.CheckoutRequest) int64 {
	if len(r.Items) == 0 {
		return r.Amount.Amount
	}
	var t int64
	for _, it := range r.Items {
		q := it.Quantity
		if q == 0 {
			q = 1
		}
		t += it.Amount.Amount * q
	}
	return t
}

func items(r payment.CheckoutRequest) []map[string]any {
	if len(r.Items) == 0 {
		return []map[string]any{{"name": "Checkout", "amount_cents": r.Amount.Amount, "quantity": 1}}
	}
	out := make([]map[string]any, 0, len(r.Items))
	for _, it := range r.Items {
		q := it.Quantity
		if q == 0 {
			q = 1
		}
		out = append(out, map[string]any{"name": it.Name, "amount_cents": it.Amount.Amount, "quantity": q})
	}
	return out
}

func ref8(r payment.ChargeRequest) string {
	if r.Customer.Email != "" {
		return r.Customer.Email
	}
	return fmt.Sprintf("%d", r.Amount.Amount)
}

func coRef(r payment.CheckoutRequest) string {
	if r.Customer.Email != "" {
		return r.Customer.Email
	}
	return fmt.Sprintf("%d", total(r))
}
