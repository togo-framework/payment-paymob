# payment-paymob

[Paymob](https://docs.paymob.com) driver for togo **payment**.

```bash
togo install togo-framework/payment
togo install togo-framework/payment-paymob
```
```env
PAYMENT_DRIVER=paymob
PAYMOB_API_KEY=...
```

Registers on the togo `payment.PaymentProvider` interface and is selected via
`PAYMENT_DRIVER=paymob`. Gateway API calls are scaffolded — see the Paymob docs.

MIT
