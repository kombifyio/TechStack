package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/discovery"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
)

type DiscoveryHandler struct {
	discovery discovery.Discovery
	mu        sync.RWMutex
	scanOwn   map[string]scanEntry
	deviceOwn map[string]deviceEntry
	stopCh    chan struct{}
}

type scanEntry struct {
	ownerID   string
	createdAt time.Time
}

type deviceEntry struct {
	ownerID   string
	createdAt time.Time
}

const discoveryCleanupInterval = 1 * time.Hour

const scanMaxAge = 24 * time.Hour

const deviceMaxAge = 7 * 24 * time.Hour

func NewDiscoveryHandler(disc discovery.Discovery) *DiscoveryHandler {
	h := &DiscoveryHandler{
		discovery: disc,
		scanOwn:   make(map[string]scanEntry),
		deviceOwn: make(map[string]deviceEntry),
		stopCh:    make(chan struct{}),
	}
	go h.cleanupLoop()
	return h
}

func (h *DiscoveryHandler) Stop() {
	close(h.stopCh)
}

func (h *DiscoveryHandler) cleanupLoop() {
	ticker := time.NewTicker(discoveryCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.cleanupOldEntries()
		case <-h.stopCh:
			return
		}
	}
}

func (h *DiscoveryHandler) cleanupOldEntries() {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()

	for scanID, entry := range h.scanOwn {
		if now.Sub(entry.createdAt) > scanMaxAge {
			delete(h.scanOwn, scanID)
		}
	}

	for deviceID, entry := range h.deviceOwn {
		if now.Sub(entry.createdAt) > deviceMaxAge {
			delete(h.deviceOwn, deviceID)
		}
	}
}

func requireAuth(e *httpx.Event) (string, error) {
	if userID, ok := authenticatedUserID(e); ok {
		return userID, nil
	}
	return "", httpx.RejectUnauthorized(e, "Authentication required")
}

func authenticatedDiscoveryUser(e *httpx.Event) (string, bool, bool, error) {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if ok {
		return ownerID, isAdmin, true, nil
	}
	return "", false, false, httpx.RejectUnauthorized(e, "Authentication required")
}

func authenticatedDiscoveryOwner(e *httpx.Event) (string, bool, error) {
	ownerID, _, ok, err := authenticatedDiscoveryUser(e)
	return ownerID, ok, err
}

func requireDiscoveryAdminAccess(e *httpx.Event) (bool, error) {
	_, isAdmin, ok, err := authenticatedDiscoveryUser(e)
	if err != nil || !ok {
		return false, err
	}
	if isAdmin {
		return true, nil
	}
	return false, httpx.RejectForbidden(e, "Admin access required")
}

func (h *DiscoveryHandler) setScanOwner(scanID, ownerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.scanOwn[scanID] = scanEntry{ownerID: ownerID, createdAt: time.Now()}
}

func (h *DiscoveryHandler) isScanOwner(scanID, ownerID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	entry, exists := h.scanOwn[scanID]
	return exists && entry.ownerID == ownerID
}

func (h *DiscoveryHandler) setDeviceOwner(deviceID, ownerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.deviceOwn[deviceID]; !exists {
		h.deviceOwn[deviceID] = deviceEntry{ownerID: ownerID, createdAt: time.Now()}
	}
}

func (h *DiscoveryHandler) isDeviceOwner(deviceID, ownerID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	entry, exists := h.deviceOwn[deviceID]
	return exists && entry.ownerID == ownerID
}

func (h *DiscoveryHandler) setDevicesOwnerFromScan(scanID, ownerID string) {
	result, ok := h.discovery.GetScanStatus(scanID)
	if !ok {
		return
	}
	h.setDevicesOwnerFromResult(result, ownerID)
}

func (h *DiscoveryHandler) setDevicesOwnerFromResult(result *discovery.ScanResult, ownerID string) {
	if result == nil || result.Devices == nil {
		return
	}
	for _, device := range result.Devices {
		h.setDeviceOwner(device.DeviceID, ownerID)
	}
}

func RegisterDiscoveryRoutes(r *httpx.Router, app core.App, handler *DiscoveryHandler) {
	if handler == nil {
		handler = NewDiscoveryHandler(discovery.New())
	}

	r.GET("/api/v1/discovery/networks", handler.networks)
	r.POST("/api/v1/discovery/scan", handler.startScan)
	r.POST("/api/v1/discovery/scan/{id}/cancel", handler.cancelScan)
	r.GET("/api/v1/discovery/scan/{id}", handler.scanStatus)
	r.POST("/api/v1/discovery/scan/{id}/enrich", handler.enrichScan)
	r.GET("/api/v1/discovery/devices", handler.listDevices)
	r.GET("/api/v1/discovery/devices/{id}", handler.getDevice)
	r.POST("/api/v1/discovery/probe", handler.probeDevice)
	r.POST("/api/v1/discovery/test-ssh", handler.testSSH)
	r.DELETE("/api/v1/discovery/cache", handler.clearCache)
	r.GET("/api/v1/discovery/stats", handler.stats)
}

func (h *DiscoveryHandler) networks(e *httpx.Event) error {
	if _, ok, err := authenticatedDiscoveryOwner(e); err != nil || !ok {
		return err
	}
	networks, err := h.discovery.GetLocalNetworks()
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to get networks", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"networks": networks})
}

func (h *DiscoveryHandler) startScan(e *httpx.Event) error {
	ownerID, ok, err := authenticatedDiscoveryOwner(e)
	if err != nil || !ok {
		return err
	}
	req, err := decodeDiscoveryScanRequest(e.Request.Body)
	if err != nil {
		return httpx.BadRequest(e, "Invalid request body")
	}
	if validationErr := validateDiscoveryScanRequest(req); validationErr != nil {
		return httpx.BadRequest(e, validationErr.Error())
	}
	result, err := h.discovery.StartScan(context.Background(), &req)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to start scan", nil)
	}
	h.setScanOwner(result.ScanID, ownerID)
	if result.Status == discovery.ScanStatusCompleted {
		h.setDevicesOwnerFromResult(result, ownerID)
	}

	return httpx.Success(e, http.StatusOK, result)
}

func (h *DiscoveryHandler) cancelScan(e *httpx.Event) error {
	ownerID, ok, err := authenticatedDiscoveryOwner(e)
	if err != nil || !ok {
		return err
	}
	scanID := e.Request.PathValue("id")
	if !h.isScanOwner(scanID, ownerID) {
		return httpx.NotFound(e, "Scan not found")
	}
	if err := h.discovery.CancelScan(scanID); err != nil {
		return httpx.Error(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Scan not found or already finished", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"canceled": true})
}

func (h *DiscoveryHandler) scanStatus(e *httpx.Event) error {
	ownerID, ok, err := authenticatedDiscoveryOwner(e)
	if err != nil || !ok {
		return err
	}
	scanID := e.Request.PathValue("id")
	if !h.isScanOwner(scanID, ownerID) {
		return httpx.NotFound(e, "Scan not found")
	}

	result, ok := h.discovery.GetScanStatus(scanID)
	if !ok {
		return httpx.NotFound(e, "Scan not found")
	}

	if result.Status == discovery.ScanStatusCompleted {
		h.setDevicesOwnerFromScan(scanID, ownerID)
	}

	return httpx.Success(e, http.StatusOK, result)
}

func (h *DiscoveryHandler) enrichScan(e *httpx.Event) error {
	ownerID, ok, err := authenticatedDiscoveryOwner(e)
	if err != nil || !ok {
		return err
	}
	scanID := e.Request.PathValue("id")
	if !h.isScanOwner(scanID, ownerID) {
		return httpx.NotFound(e, "Scan not found")
	}

	result, err := h.discovery.EnrichScan(scanID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to enrich scan", nil)
	}
	h.setDevicesOwnerFromResult(result, ownerID)

	return httpx.Success(e, http.StatusOK, result)
}

func (h *DiscoveryHandler) listDevices(e *httpx.Event) error {
	ownerID, ok, err := authenticatedDiscoveryOwner(e)
	if err != nil || !ok {
		return err
	}
	allDevices := h.discovery.GetDevices()

	devices := make([]*discovery.DiscoveredDevice, 0, len(allDevices))
	for _, device := range allDevices {
		if h.isDeviceOwner(device.DeviceID, ownerID) {
			devices = append(devices, device)
		}
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"devices": devices, "count": len(devices)})
}

func (h *DiscoveryHandler) getDevice(e *httpx.Event) error {
	ownerID, ok, err := authenticatedDiscoveryOwner(e)
	if err != nil || !ok {
		return err
	}
	deviceID := e.Request.PathValue("id")
	if !h.isDeviceOwner(deviceID, ownerID) {
		return httpx.NotFound(e, "Device not found")
	}

	device, ok := h.discovery.GetDevice(deviceID)
	if !ok {
		return httpx.NotFound(e, "Device not found")
	}

	return httpx.Success(e, http.StatusOK, device)
}

func (h *DiscoveryHandler) probeDevice(e *httpx.Event) error {
	ownerID, isAdmin, ok, err := authenticatedDiscoveryUser(e)
	if err != nil || !ok {
		return err
	}
	req, err := decodeDiscoveryProbeRequest(e.Request.Body)
	if err != nil {
		return httpx.BadRequest(e, "Invalid request body")
	}
	if validationErr := validateDiscoveryProbeRequest(req); validationErr != nil {
		return httpx.BadRequest(e, validationErr.Error())
	}
	if accessOK, accessErr := h.requireDeviceIPAccess(e, ownerID, isAdmin, req.IP); accessErr != nil || !accessOK {
		return accessErr
	}

	result, err := h.discovery.ProbeDevice(e.Request.Context(), &req)
	if err != nil {
		return httpx.Success(e, http.StatusOK, result)
	}

	return httpx.Success(e, http.StatusOK, result)
}

func (h *DiscoveryHandler) testSSH(e *httpx.Event) error {
	ownerID, isAdmin, ok, err := authenticatedDiscoveryUser(e)
	if err != nil || !ok {
		return err
	}
	req, err := decodeDiscoveryTestSSHRequest(e.Request.Body)
	if err != nil {
		return httpx.BadRequest(e, "Invalid request body")
	}
	if validationErr := validateDiscoveryTestSSHRequest(req); validationErr != nil {
		return httpx.BadRequest(e, validationErr.Error())
	}
	if accessOK, accessErr := h.requireDeviceIPAccess(e, ownerID, isAdmin, req.IP); accessErr != nil || !accessOK {
		return accessErr
	}

	err = h.discovery.TestSSH(
		e.Request.Context(),
		req.IP,
		req.Port,
		req.User,
		req.Password,
	)

	if err != nil {
		return httpx.Success(e, http.StatusOK, map[string]any{routeSuccessField: false, routeErrorField: err.Error()})
	}
	return httpx.Success(e, http.StatusOK, map[string]any{routeSuccessField: true, routeMessageField: "SSH connection successful"})
}

func (h *DiscoveryHandler) clearCache(e *httpx.Event) error {
	if ok, err := requireDiscoveryAdminAccess(e); err != nil || !ok {
		return err
	}
	h.discovery.ClearCache()
	return httpx.Success(e, http.StatusOK, map[string]any{routeSuccessField: true, routeMessageField: "Discovery cache cleared"})
}

func (h *DiscoveryHandler) stats(e *httpx.Event) error {
	if ok, err := requireDiscoveryAdminAccess(e); err != nil || !ok {
		return err
	}
	stats := h.discovery.GetStats()
	return httpx.Success(e, http.StatusOK, stats)
}

func (h *DiscoveryHandler) requireDeviceIPAccess(e *httpx.Event, ownerID string, isAdmin bool, ip string) (bool, error) {
	if isAdmin {
		return true, nil
	}
	device, ok := h.discovery.GetDeviceByIP(ip)
	if !ok || !h.isDeviceOwner(device.DeviceID, ownerID) {
		return false, httpx.NotFound(e, "Device not found")
	}
	return true, nil
}

func decodeDiscoveryScanRequest(body io.Reader) (discovery.ScanRequest, error) {
	var req discovery.ScanRequest
	err := json.NewDecoder(body).Decode(&req)
	if err == nil {
		return req, nil
	}
	if errors.Is(err, io.EOF) {
		return discovery.ScanRequest{Profile: discovery.ProfileActive}, nil
	}
	return discovery.ScanRequest{}, err
}

func validateDiscoveryScanRequest(req discovery.ScanRequest) error {
	if req.Subnet != "" {
		ip, _, err := net.ParseCIDR(req.Subnet)
		if err != nil || ip == nil || ip.To4() == nil {
			return errors.New("subnet must be a valid IPv4 CIDR")
		}
	}
	for _, ipStr := range req.TargetIPs {
		if net.ParseIP(ipStr) == nil {
			return errors.New("target_ips must contain valid IP addresses")
		}
	}
	for _, ipStr := range req.ExcludeIPs {
		if net.ParseIP(ipStr) == nil {
			return errors.New("exclude_ips must contain valid IP addresses")
		}
	}
	if req.TimeoutSeconds < 0 || req.MaxConcurrent < 0 {
		return errors.New("timeout_seconds and max_concurrent must be >= 0")
	}
	return nil
}

func decodeDiscoveryProbeRequest(body io.Reader) (discovery.ProbeRequest, error) {
	var req discovery.ProbeRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return discovery.ProbeRequest{}, err
	}
	return req, nil
}

func validateDiscoveryProbeRequest(req discovery.ProbeRequest) error {
	if req.IP == "" {
		return errors.New("IP address is required")
	}
	if net.ParseIP(req.IP) == nil {
		return errors.New("IP address is invalid")
	}
	if req.SSHUser == "" {
		return errors.New("SSH user is required for deep probe")
	}
	return nil
}

type discoveryTestSSHRequest struct {
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
}

func decodeDiscoveryTestSSHRequest(body io.Reader) (discoveryTestSSHRequest, error) {
	var req discoveryTestSSHRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return discoveryTestSSHRequest{}, err
	}
	if req.Port == 0 {
		req.Port = 22
	}
	return req, nil
}

func validateDiscoveryTestSSHRequest(req discoveryTestSSHRequest) error {
	if req.IP == "" || req.User == "" {
		return errors.New("IP and user are required")
	}
	return nil
}
