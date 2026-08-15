package routes

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

const (
	inventoryCursorField = "cursor"
	inventoryLimitField  = "limit"
	inventoryCursorV1    = 1
	maxInventoryCursor   = 2048
)

type inventoryPageOptions struct {
	Limit  int
	Cursor string
}

type inventoryCursorPayload struct {
	Version     int    `json:"v"`
	Kind        string `json:"kind"`
	Binding     string `json:"binding"`
	FrozenEmpty bool   `json:"frozen_empty,omitempty"`
	WatermarkAt string `json:"watermark_at,omitempty"`
	WatermarkID string `json:"watermark_id,omitempty"`
	AfterAt     string `json:"after_at,omitempty"`
	AfterID     string `json:"after_id,omitempty"`
}

func inventoryPageFromQuery(values map[string][]string) (inventoryPageOptions, error) {
	options := inventoryPageOptions{Limit: controlplane.DefaultInventoryPageSize}
	if raw := strings.TrimSpace(firstQueryValue(values, inventoryLimitField)); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > controlplane.MaxInventoryPageSize {
			return inventoryPageOptions{}, inventoryValidationError("invalid_page_limit", "Inventory page limit is invalid")
		}
		options.Limit = limit
	}
	options.Cursor = strings.TrimSpace(firstQueryValue(values, inventoryCursorField))
	if len(options.Cursor) > maxInventoryCursor {
		return inventoryPageOptions{}, inventoryValidationError("invalid_cursor", "Inventory cursor is invalid")
	}
	return options, nil
}

func firstQueryValue(values map[string][]string, key string) string {
	if len(values[key]) == 0 {
		return ""
	}
	return values[key][0]
}

func inventoryPageRequest(kind, scopeKey string, options inventoryPageOptions) (controlplane.InventoryPageRequest, error) {
	request := controlplane.InventoryPageRequest{Limit: options.Limit}
	if request.Limit <= 0 {
		request.Limit = controlplane.DefaultInventoryPageSize
	}
	if strings.TrimSpace(options.Cursor) == "" {
		return request, nil
	}
	payload, err := decodeInventoryCursor(kind, scopeKey, options.Cursor)
	if err != nil {
		return controlplane.InventoryPageRequest{}, err
	}
	request.Watermark = payload.Watermark
	request.After = payload.After
	request.FrozenEmpty = payload.FrozenEmpty
	return request, nil
}

type decodedInventoryCursor struct {
	Watermark   controlplane.InventoryPageKey
	After       controlplane.InventoryPageKey
	FrozenEmpty bool
}

func decodeInventoryCursor(kind, scopeKey, encoded string) (decodedInventoryCursor, error) {
	if encoded == "" || len(encoded) > maxInventoryCursor {
		return decodedInventoryCursor{}, inventoryValidationError("invalid_cursor", "Inventory cursor is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return decodedInventoryCursor{}, inventoryValidationError("invalid_cursor", "Inventory cursor is invalid")
	}
	var payload inventoryCursorPayload
	decoderErr := json.Unmarshal(raw, &payload)
	if decoderErr != nil || payload.Version != inventoryCursorV1 || payload.Kind != kind || payload.Binding != inventoryCursorBinding(kind, scopeKey) {
		return decodedInventoryCursor{}, inventoryValidationError("invalid_cursor", "Inventory cursor is invalid")
	}
	watermark, err := inventoryCursorKey(payload.WatermarkAt, payload.WatermarkID)
	if err != nil {
		return decodedInventoryCursor{}, err
	}
	after, err := inventoryCursorKey(payload.AfterAt, payload.AfterID)
	if err != nil || !validInventoryCursorPosition(payload.FrozenEmpty, watermark, after) {
		return decodedInventoryCursor{}, inventoryValidationError("invalid_cursor", "Inventory cursor is invalid")
	}
	return decodedInventoryCursor{Watermark: watermark, After: after, FrozenEmpty: payload.FrozenEmpty}, nil
}

func validInventoryCursorPosition(frozenEmpty bool, watermark, after controlplane.InventoryPageKey) bool {
	if frozenEmpty {
		return watermark.IsZero() && after.IsZero()
	}
	if watermark.IsZero() {
		return false
	}
	return after.IsZero() || compareInventoryCursorKeys(after, watermark) <= 0
}

func inventoryCursorKey(at, id string) (controlplane.InventoryPageKey, error) {
	at, id = strings.TrimSpace(at), strings.TrimSpace(id)
	if at == "" && id == "" {
		return controlplane.InventoryPageKey{}, nil
	}
	if at == "" || id == "" || len(id) > 256 {
		return controlplane.InventoryPageKey{}, inventoryValidationError("invalid_cursor", "Inventory cursor is invalid")
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return controlplane.InventoryPageKey{}, inventoryValidationError("invalid_cursor", "Inventory cursor is invalid")
	}
	return controlplane.InventoryPageKey{CreatedAt: parsed.UTC(), ID: id}, nil
}

func encodeInventoryCursor(kind, scopeKey string, watermark controlplane.InventoryPageKey, after *controlplane.InventoryPageKey) string {
	payload := inventoryCursorPayload{Version: inventoryCursorV1, Kind: kind, Binding: inventoryCursorBinding(kind, scopeKey)}
	if watermark.IsZero() {
		payload.FrozenEmpty = true
	}
	if !watermark.IsZero() {
		payload.WatermarkAt = watermark.CreatedAt.UTC().Format(time.RFC3339Nano)
		payload.WatermarkID = watermark.ID
	}
	if after != nil && !after.IsZero() {
		payload.AfterAt = after.CreatedAt.UTC().Format(time.RFC3339Nano)
		payload.AfterID = after.ID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func encodeInventoryNextCursor(kind, scopeKey string, watermark controlplane.InventoryPageKey, after *controlplane.InventoryPageKey) string {
	if after == nil || after.IsZero() {
		return ""
	}
	return encodeInventoryCursor(kind, scopeKey, watermark, after)
}

func inventoryCursorBinding(kind, scopeKey string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(kind) + "\x00" + strings.TrimSpace(scopeKey)))
	return hex.EncodeToString(digest[:])
}

func inventoryCursorScopeKey(scope controlplane.InventoryReadScope, filter string) string {
	targetType, targetID := scope.Target()
	return strings.Join([]string{
		scope.TenantID(), scope.OwnerSubjectID(), targetType, targetID, strings.TrimSpace(filter),
	}, "\x00")
}

func compareInventoryCursorKeys(left, right controlplane.InventoryPageKey) int {
	switch {
	case left.CreatedAt.Before(right.CreatedAt):
		return -1
	case left.CreatedAt.After(right.CreatedAt):
		return 1
	case left.ID < right.ID:
		return -1
	case left.ID > right.ID:
		return 1
	default:
		return 0
	}
}

func inventoryValidationError(reason, message string) *inventoryError {
	return &inventoryError{status: http.StatusBadRequest, reasonCode: reason, message: message}
}
