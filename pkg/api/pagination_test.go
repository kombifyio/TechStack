package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParsePagination(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected PaginationParams
	}{
		{
			name:     "default values",
			query:    "",
			expected: PaginationParams{Page: 1, PerPage: 20, Offset: 0},
		},
		{
			name:     "page and per_page",
			query:    "?page=2&per_page=10",
			expected: PaginationParams{Page: 2, PerPage: 10, Offset: 10},
		},
		{
			name:     "page only",
			query:    "?page=3",
			expected: PaginationParams{Page: 3, PerPage: 20, Offset: 40},
		},
		{
			name:     "per_page over max",
			query:    "?per_page=200",
			expected: PaginationParams{Page: 1, PerPage: 100, Offset: 0},
		},
		{
			name:     "legacy offset/limit",
			query:    "?offset=20&limit=10",
			expected: PaginationParams{Page: 3, PerPage: 10, Offset: 20},
		},
		{
			name:     "invalid values default",
			query:    "?page=abc&per_page=-1",
			expected: PaginationParams{Page: 1, PerPage: 20, Offset: 0},
		},
		{
			name:     "zero page defaults to 1",
			query:    "?page=0",
			expected: PaginationParams{Page: 1, PerPage: 20, Offset: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test"+tt.query, nil)
			got := ParsePagination(req)

			if got.Page != tt.expected.Page {
				t.Errorf("Page = %d, want %d", got.Page, tt.expected.Page)
			}
			if got.PerPage != tt.expected.PerPage {
				t.Errorf("PerPage = %d, want %d", got.PerPage, tt.expected.PerPage)
			}
			if got.Offset != tt.expected.Offset {
				t.Errorf("Offset = %d, want %d", got.Offset, tt.expected.Offset)
			}
		})
	}
}

func TestPaginate(t *testing.T) {
	items := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	tests := []struct {
		name     string
		params   PaginationParams
		expected []int
	}{
		{
			name:     "first page",
			params:   PaginationParams{Page: 1, PerPage: 3, Offset: 0},
			expected: []int{1, 2, 3},
		},
		{
			name:     "second page",
			params:   PaginationParams{Page: 2, PerPage: 3, Offset: 3},
			expected: []int{4, 5, 6},
		},
		{
			name:     "last page partial",
			params:   PaginationParams{Page: 4, PerPage: 3, Offset: 9},
			expected: []int{10},
		},
		{
			name:     "beyond last page",
			params:   PaginationParams{Page: 5, PerPage: 3, Offset: 12},
			expected: []int{},
		},
		{
			name:     "all items on one page",
			params:   PaginationParams{Page: 1, PerPage: 20, Offset: 0},
			expected: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Paginate(items, tt.params)

			if len(got) != len(tt.expected) {
				t.Errorf("len = %d, want %d", len(got), len(tt.expected))
				return
			}

			for i, v := range got {
				if v != tt.expected[i] {
					t.Errorf("item[%d] = %d, want %d", i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestNewPaginatedMeta(t *testing.T) {
	meta := NewPaginatedMeta(100, 2, 10)

	if meta.Total != 100 {
		t.Errorf("Total = %d, want 100", meta.Total)
	}
	if meta.Page != 2 {
		t.Errorf("Page = %d, want 2", meta.Page)
	}
	if meta.PerPage != 10 {
		t.Errorf("PerPage = %d, want 10", meta.PerPage)
	}
}
