# payment-paymob — Paymob driver for togo

`payment-paymob` is the **Paymob** driver for the togo [`payment`](https://github.com/togo-framework/payment) subsystem. It implements the `payment.PaymentProvider` contract against the Paymob API.

- **Coverage:** Egypt / MENA · EGP
- **Gateway API docs:** https://developers.paymob.com
- **Marketplace:** https://to-go.dev/marketplace

## Install

```bash
togo install togo-framework/payment        # the base (once)
togo install togo-framework/payment-paymob   # this driver
```

Select the driver at runtime:

```env
PAYMENT_DRIVER=paymob
```

## Configuration

| Env | Required | Description |
|---|---|---|
| `PAYMOB_API_KEY` | **yes** | Paymob API key (used for the auth-token step). |
| `PAYMOB_INTEGRATION_ID` | **yes** | Integration/payment-method id. |
| `PAYMOB_HMAC` | **yes** | HMAC secret for webhook verification. |
| `PAYMOB_IFRAME_ID` | no | iframe id for the hosted card page. |
| `PAYMOB_BASE_URL` | no | API base override (default `https://accept.paymob.com`). |

## Usage (Go)

The base plugin stores a `*payment.Service` on the kernel. Get it with `payment.FromKernel`:

```go
import "github.com/togo-framework/payment"

svc, ok := payment.FromKernel(k)
if !ok {
    // payment plugin not installed / not booted
}

// One-off charge (Token comes from the gateway's client SDK / a saved source):
charge, err := svc.CreateCharge(ctx, payment.ChargeRequest{
    Amount:      payment.Money{Value: 1000, Currency: "USD"}, // smallest unit
    Customer:    payment.Customer{Email: "buyer@example.com"},
    Token:       "<gateway-token>",
    Description: "Order #1001",
    Metadata:    map[string]string{"order_id": "1001"},
})

// Hosted checkout — redirect the buyer to the returned URL:
sess, err := svc.CreateCheckoutSession(ctx, payment.CheckoutRequest{
    Amount:     payment.Money{Value: 1000, Currency: "USD"},
    Items:      []payment.LineItem{{Name: "Pro plan", Amount: payment.Money{Value: 1000, Currency: "USD"}, Quantity: 1}},
    SuccessURL: "https://app.example.com/success",
    CancelURL:  "https://app.example.com/cancel",
})
// http.Redirect(w, r, sess.URL, http.StatusSeeOther)

// Refund (full when Amount is nil, else partial):
err = svc.Refund(ctx, payment.RefundRequest{ /* charge id, optional Amount */ })
```

## Webhooks

Point your Paymob webhook at a route in your app, then hand the **raw body + headers** to the service — the driver does the rest:

```go
ev, err := svc.HandleWebhook(ctx, headers, rawBody)
if err != nil {
    http.Error(w, "invalid webhook", http.StatusBadRequest)
    return
}
// ev.Type, ev.ID, ev.Provider, ev.Raw
```

**Verification:** this driver verifies **an **HMAC-SHA512** over the documented field order**. Set `PAYMOB_HMAC` (your Paymob HMAC secret) to enable verification. Forged or tampered webhooks are rejected; with no secret configured it stays parse-only for local dev.

## Supported methods

| `PaymentProvider` method | Status |
|---|---|
| `CreateCharge` | ✅ |
| `Refund` | ✅ |
| `CreateCheckoutSession` | ✅ |
| `HandleWebhook` | ✅ (verified) |
| `CreateCustomer` / `CreateSubscription` | Supported where Paymob offers it natively; otherwise returns a clear, documented error (see the driver source). |

## Links

- **Source:** https://github.com/togo-framework/payment-paymob
- **Base plugin:** https://github.com/togo-framework/payment
- **Paymob docs:** https://developers.paymob.com
