package httpx

import (
	"errors"
	"net/http"
	"testing"

	"github.com/pocketbase/pocketbase/tools/router"
)

func TestStatusOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"plain error -> 500", errors.New("boom"), 500},
		{"httpx 404", NewNotFoundError("missing", nil), http.StatusNotFound},
		{"httpx 400", NewBadRequestError("bad", nil), http.StatusBadRequest},
		{"httpx 500", NewInternalServerError("oops", nil), http.StatusInternalServerError},
		{"router 404", &router.ApiError{Status: http.StatusNotFound}, http.StatusNotFound},
		{"router 500", &router.ApiError{Status: http.StatusInternalServerError}, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusOf(tc.err); got != tc.want {
				t.Fatalf("StatusOf(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsServerError(t *testing.T) {
	serverErrors := []error{
		errors.New("boom"),
		NewInternalServerError("oops", nil),
		&router.ApiError{Status: http.StatusBadGateway},
	}
	for _, err := range serverErrors {
		if !IsServerError(err) {
			t.Fatalf("IsServerError(%v) = false, want true", err)
		}
	}

	clientErrors := []error{
		nil,
		NewNotFoundError("missing", nil),
		NewBadRequestError("bad", nil),
		NewUnauthorizedError("", nil),
		&router.ApiError{Status: http.StatusNotFound},
	}
	for _, err := range clientErrors {
		if IsServerError(err) {
			t.Fatalf("IsServerError(%v) = true, want false", err)
		}
	}

	// A wrapped 404 must still be treated as a client error.
	wrapped := errors.Join(NewNotFoundError("missing", nil))
	if IsServerError(wrapped) {
		t.Fatalf("IsServerError(wrapped 404) = true, want false")
	}
}
