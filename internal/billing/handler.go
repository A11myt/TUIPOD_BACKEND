package billing

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	stripe "github.com/stripe/stripe-go/v82"
	checkoutsession "github.com/stripe/stripe-go/v82/checkout/session"
	portalsession "github.com/stripe/stripe-go/v82/billingportal/session"
	"github.com/stripe/stripe-go/v82/webhook"

	"github.com/A11myt/tuipod/internal/auth"
	"github.com/A11myt/tuipod/internal/httperr"
)

type Handler struct {
	db *pgxpool.Pool
}

// NewHandler constructs a billing Handler backed by the given DB pool.
func NewHandler(db *pgxpool.Pool) *Handler {
	return &Handler{db: db}
}

// stripeEnabled reports whether Stripe billing is configured.
func stripeEnabled() bool { return os.Getenv("STRIPE_SECRET_KEY") != "" }

type statusResponse struct {
	Plan             string  `json:"plan"`
	StripeCustomerID *string `json:"stripe_customer_id,omitempty"`
}

// Status returns the plan and Stripe customer ID of the logged-in user.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r)
	var s statusResponse
	h.db.QueryRow(r.Context(),
		`SELECT plan, stripe_customer_id FROM users WHERE id = $1`, userID,
	).Scan(&s.Plan, &s.StripeCustomerID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

type checkoutRequest struct {
	Plan string `json:"plan"`
}

// Checkout creates a Stripe checkout session for the basic/pro plan; returns 501 if
// billing isn't configured.
func (h *Handler) Checkout(w http.ResponseWriter, r *http.Request) {
	if !stripeEnabled() {
		httperr.Write(w, http.StatusNotImplemented, "billing not configured")
		return
	}
	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")

	userID := auth.UserIDFromCtx(r)
	var req checkoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, http.StatusBadRequest, "invalid body")
		return
	}

	var priceID string
	switch req.Plan {
	case "basic":
		priceID = os.Getenv("STRIPE_PRICE_BASIC")
	case "pro":
		priceID = os.Getenv("STRIPE_PRICE_PRO")
	default:
		httperr.Write(w, http.StatusBadRequest, "plan must be basic or pro")
		return
	}
	if priceID == "" {
		httperr.Write(w, http.StatusInternalServerError, "price not configured")
		return
	}

	appURL := os.Getenv("APP_URL")
	params := &stripe.CheckoutSessionParams{
		Mode:              stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		ClientReferenceID: stripe.String(userID),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{Price: stripe.String(priceID), Quantity: stripe.Int64(1)},
		},
		SuccessURL: stripe.String(appURL + "/billing/success"),
		CancelURL:  stripe.String(appURL + "/billing/cancel"),
		Metadata:   map[string]string{"plan": req.Plan},
	}

	sess, err := checkoutsession.New(params)
	if err != nil {
		slog.Error("stripe checkout", "err", err)
		httperr.Write(w, http.StatusInternalServerError, "could not create checkout session")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": sess.URL})
}

// Portal creates a Stripe billing portal session so the user can self-manage their subscription.
func (h *Handler) Portal(w http.ResponseWriter, r *http.Request) {
	if !stripeEnabled() {
		httperr.Write(w, http.StatusNotImplemented, "billing not configured")
		return
	}
	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")

	userID := auth.UserIDFromCtx(r)
	var customerID *string
	if err := h.db.QueryRow(r.Context(),
		`SELECT stripe_customer_id FROM users WHERE id = $1`, userID,
	).Scan(&customerID); err != nil || customerID == nil || *customerID == "" {
		httperr.Write(w, http.StatusBadRequest, "no active subscription")
		return
	}

	params := &stripe.BillingPortalSessionParams{
		Customer:  stripe.String(*customerID),
		ReturnURL: stripe.String(os.Getenv("APP_URL") + "/settings"),
	}
	sess, err := portalsession.New(params)
	if err != nil {
		slog.Error("stripe portal", "err", err)
		httperr.Write(w, http.StatusInternalServerError, "could not create portal session")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": sess.URL})
}

// Webhook handles Stripe events (signature-checked, idempotent via the stripe_events table):
// checkout completed sets the plan, subscription updated adjusts it, subscription deleted
// downgrades to free. Must be registered without auth middleware.
func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, 65536))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	event, err := webhook.ConstructEvent(payload,
		r.Header.Get("Stripe-Signature"),
		os.Getenv("STRIPE_WEBHOOK_SECRET"),
	)
	if err != nil {
		slog.Warn("stripe webhook signature invalid", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// idempotency: skip already-processed events
	if _, err := h.db.Exec(r.Context(),
		`INSERT INTO stripe_events (id) VALUES ($1) ON CONFLICT DO NOTHING`, event.ID,
	); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	switch event.Type {
	case "checkout.session.completed":
		var cs stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &cs); err != nil {
			break
		}
		plan := cs.Metadata["plan"]
		if cs.ClientReferenceID == "" || plan == "" {
			break
		}
		customerID := ""
		if cs.Customer != nil {
			customerID = cs.Customer.ID
		}
		h.db.Exec(r.Context(), `
			UPDATE users SET plan = $1, stripe_customer_id = $2 WHERE id = $3`,
			plan, customerID, cs.ClientReferenceID,
		)
		slog.Info("billing upgraded", "user", cs.ClientReferenceID, "plan", plan)

	case "customer.subscription.updated":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			break
		}
		if sub.Customer == nil || len(sub.Items.Data) == 0 {
			break
		}
		priceID := sub.Items.Data[0].Price.ID
		plan := priceToplan(priceID)
		if plan == "" {
			break
		}
		h.db.Exec(r.Context(),
			`UPDATE users SET plan = $1 WHERE stripe_customer_id = $2`,
			plan, sub.Customer.ID,
		)

	case "customer.subscription.deleted":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			break
		}
		if sub.Customer == nil {
			break
		}
		h.db.Exec(r.Context(),
			`UPDATE users SET plan = 'free' WHERE stripe_customer_id = $1`,
			sub.Customer.ID,
		)
		slog.Info("billing downgraded to free", "customer", sub.Customer.ID)
	}

	w.WriteHeader(http.StatusNoContent)
}

// priceToplan maps a Stripe price ID (from env) to the internal plan name.
func priceToplan(priceID string) string {
	switch priceID {
	case os.Getenv("STRIPE_PRICE_BASIC"):
		return "basic"
	case os.Getenv("STRIPE_PRICE_PRO"):
		return "pro"
	default:
		return ""
	}
}
