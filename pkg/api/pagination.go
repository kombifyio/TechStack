// Package api provides HTTP handlers and utilities for the kombifyTechstack API
package api

import (
	"net/http"
	"strconv"
)

const (
	// DefaultPage is the default page number
	DefaultPage = 1
	// DefaultPerPage is the default number of items per page
	DefaultPerPage = 20
	// MaxPerPage is the maximum number of items per page
	MaxPerPage = 100
)

// PaginationParams holds pagination parameters extracted from a request
type PaginationParams struct {
	Page    int
	PerPage int
	Offset  int
}

// ParsePagination extracts pagination parameters from query string.
// Supports both ?page=N&per_page=M and legacy ?offset=N&limit=M formats.
func ParsePagination(r *http.Request) PaginationParams {
	page := parseIntParam(r, "page", DefaultPage)
	perPage := parseIntParam(r, "per_page", DefaultPerPage)

	// Support legacy offset/limit format
	if offset := parseIntParam(r, "offset", -1); offset >= 0 {
		limit := parseIntParam(r, "limit", DefaultPerPage)
		return PaginationParams{
			Page:    (offset / limit) + 1,
			PerPage: limit,
			Offset:  offset,
		}
	}

	// Validate bounds
	if page < 1 {
		page = DefaultPage
	}
	if perPage < 1 {
		perPage = DefaultPerPage
	}
	if perPage > MaxPerPage {
		perPage = MaxPerPage
	}

	return PaginationParams{
		Page:    page,
		PerPage: perPage,
		Offset:  (page - 1) * perPage,
	}
}

// parseIntParam extracts an integer from query string with a default value
func parseIntParam(r *http.Request, key string, defaultVal int) int {
	val := r.URL.Query().Get(key)
	if val == "" {
		return defaultVal
	}
	i, err := strconv.Atoi(val)
	if err != nil {
		return defaultVal
	}
	return i
}

// Paginate applies pagination to a slice and returns the paginated result
func Paginate[T any](items []T, params PaginationParams) []T {
	total := len(items)
	start := params.Offset
	end := start + params.PerPage

	if start >= total {
		return []T{}
	}
	if end > total {
		end = total
	}

	return items[start:end]
}

// NewPaginatedMeta creates a ResponseMeta with pagination info
func NewPaginatedMeta(total, page, perPage int) *ResponseMeta {
	return &ResponseMeta{
		Total:   total,
		Page:    page,
		PerPage: perPage,
	}
}
