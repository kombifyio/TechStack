// Package datasubject is Techstack's participant endpoint for the Cloud
// data-subject coordinator (kombify-runtime-contracts-go datasubject). Cloud
// owns the account and the opaque SubjectRef; Techstack exports and erases only
// the control-plane rows it holds for that subject and answers a receipt signed
// with its own Ed25519 key. Live provider resources block erasure: Techstack
// decommissions them first. The endpoint is service-authenticated (bound
// service auth, caller "gateway", scope datasubject:execute) and closed unless
// explicitly enabled.
package datasubject

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	contract "github.com/kombifyio/techstack/internal/selfhostcontracts/datasubject"
	"github.com/kombifyio/techstack/pkg/serviceauth"
)

const (
	// ServiceName is the participant name Cloud registers and verifies.
	ServiceName = "techstack"
	// Path is the service-only route.
	Path = "/internal/v1/datasubject/requests"
	// Scope is the signed service scope the caller must carry.
	Scope = "datasubject:execute"

	maxBody = 16 << 10
	// ReasonExecutorNotWired is reported until an Executor exists.
	ReasonExecutorNotWired = "techstack_executor_not_wired"
	reasonExecutionFailed  = "techstack_execution_failed"
)

// ErrNothingHeld is returned by an Executor that holds no data for the subject.
var ErrNothingHeld = errors.New("datasubject: nothing held")

// BlockedError makes the receipt blocked with a stable reason code.
type BlockedError struct{ Reason string }

func (e BlockedError) Error() string { return "datasubject: blocked: " + e.Reason }

// Outcome is what an Executor did. Retained data (backups, logs, provider
// evidence) is listed with retained_until and never reported as erased.
type Outcome struct {
	Datasets []contract.Dataset
	Retained []contract.Retained
}

// Executor resolves the opaque subject to Techstack's own data and acts on it.
// Both methods are idempotent on the same subject: Cloud redelivers.
type Executor interface {
	Export(ctx context.Context, subjectRef string) (Outcome, error)
	Erase(ctx context.Context, subjectRef string) (Outcome, error)
}

// Config configures the handler.
type Config struct {
	Enabled            bool
	Secret, SecretNext string
	SigningKey         ed25519.PrivateKey
	Executor           Executor
	Now                func() time.Time
}

// Unavailable reserves the route while the endpoint is not configured.
func Unavailable() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "data subject endpoint unavailable", http.StatusServiceUnavailable)
	})
}

// NewHandler returns the participant endpoint. Missing enablement, secret or
// signing key leaves the route closed (fail closed).
func NewHandler(cfg Config) http.Handler {
	if !cfg.Enabled || !serviceauth.CanVerify(cfg.Secret) || len(cfg.SigningKey) != ed25519.PrivateKeySize {
		return Unavailable()
	}
	if cfg.Executor == nil {
		cfg.Executor = unwired{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	h := &handler{cfg: cfg}
	authCfg, err := serviceauth.Apply(servicecall.Config{Enabled: true, ServiceName: ServiceName, Secret: cfg.Secret,
		SecretNext: cfg.SecretNext, AllowedCallers: []string{"gateway"}})
	if err != nil {
		return Unavailable()
	}
	bound := servicecall.RequireBoundServiceAuth(servicecall.BoundRequestConfig{
		Config:        authCfg,
		RequiredScope: Scope, MaxBodyBytes: maxBody,
	})(http.HandlerFunc(h.serve))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		bound.ServeHTTP(w, r)
	})
}

type unwired struct{}

func (unwired) Export(context.Context, string) (Outcome, error) {
	return Outcome{}, BlockedError{Reason: ReasonExecutorNotWired}
}
func (unwired) Erase(context.Context, string) (Outcome, error) {
	return Outcome{}, BlockedError{Reason: ReasonExecutorNotWired}
}

type handler struct{ cfg Config }

func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Method != http.MethodPost || r.URL.Path != Path || err != nil || media != "application/json" {
		http.Error(w, "invalid data subject request", http.StatusBadRequest)
		return
	}
	var req contract.Request
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	// An unknown shape or a non-opaque SubjectRef (for example an e-mail
	// address) is refused before any store is touched.
	if err := dec.Decode(&req); err != nil || req.Validate() != nil {
		http.Error(w, "invalid data subject request", http.StatusBadRequest)
		return
	}
	caller := servicecall.FromContext(r.Context())
	if caller == nil || caller.Service != "gateway" || caller.Scope != Scope || caller.OnBehalfOf != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	receipt := contract.Receipt{
		RequestID: req.RequestID, Service: ServiceName, Kind: req.Kind,
		CompletedAt: h.cfg.Now().UTC(),
		Datasets:    []contract.Dataset{}, Retained: []contract.Retained{}, Processors: []contract.Processor{},
	}
	var out Outcome
	if req.Kind == contract.KindExport {
		out, err = h.cfg.Executor.Export(r.Context(), req.SubjectRef)
	} else {
		out, err = h.cfg.Executor.Erase(r.Context(), req.SubjectRef)
	}
	var blocked BlockedError
	switch {
	case err == nil:
		receipt.State = contract.ReceiptErased
		if req.Kind == contract.KindExport {
			receipt.State = contract.ReceiptExported
		}
	case errors.Is(err, ErrNothingHeld):
		receipt.State = contract.ReceiptNothingHeld
	case errors.As(err, &blocked):
		receipt.State, receipt.ReasonCode = contract.ReceiptBlocked, &blocked.Reason
	default:
		reason := reasonExecutionFailed
		receipt.State, receipt.ReasonCode = contract.ReceiptFailed, &reason
	}
	if out.Datasets != nil {
		receipt.Datasets = out.Datasets
	}
	if out.Retained != nil {
		receipt.Retained = out.Retained
	}
	sealed, err := receipt.Seal(h.cfg.SigningKey)
	if err != nil {
		http.Error(w, "receipt unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sealed)
}
