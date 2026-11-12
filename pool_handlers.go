// pool_handlers.go wires the pool management endpoints registered in
// management.go. It is deliberately separate from management.go so the pool
// feature can be reviewed (and disabled) in one place.
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// handlePoolConfig reads or writes the runtime pool toggles.
func handlePoolConfig(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		MinAvailable *int `json:"min_available_accounts"`
	}
	_ = json.Unmarshal(req.Body, &body)
	if body.MinAvailable != nil {
		poolSetMinAvailable(*body.MinAvailable)
	}
	cfg := poolConfigSnapshot()
	available, _ := poolAvailableCount()
	out := map[string]any{
		"min_available_accounts": cfg.MinAvailable,
		"available":              available,
		"healthy":                available >= cfg.MinAvailable,
		"persistent":             false,
	}
	if body.MinAvailable != nil {
		// A raised minimum must be honored immediately, not on the next tick.
		go poolEnsureMinimum("config")
	}
	return out
}

// handlePoolCheck forces a credit refresh and re-evaluates the invariant.
func handlePoolCheck(req pluginapi.ManagementRequest) map[string]any {
	var body struct {
		Force *bool `json:"force"`
	}
	_ = json.Unmarshal(req.Body, &body)
	force := true
	if body.Force != nil {
		force = *body.Force
	}
	refresh := poolRefreshAllCredits(force)
	payload := poolStatePayload()
	payload["refresh"] = refresh
	return payload
}

// handlePoolQuarantine parks one account.
func handlePoolQuarantine(req pluginapi.ManagementRequest) (map[string]any, error) {
	var body struct {
		AuthIndex string `json:"auth_index"`
		Minutes   int    `json:"minutes"`
		Reason    string `json:"reason"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		return nil, fmt.Errorf("auth_index is required")
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "manual"
	}
	return poolQuarantine(authIndex, body.Minutes, reason)
}

// handlePoolRelease clears a quarantine.
func handlePoolRelease(req pluginapi.ManagementRequest) (map[string]any, error) {
	var body struct {
		AuthIndex string `json:"auth_index"`
	}
	_ = json.Unmarshal(req.Body, &body)
	authIndex := strings.TrimSpace(body.AuthIndex)
	if authIndex == "" {
		return nil, fmt.Errorf("auth_index is required")
	}
	return poolRelease(authIndex)
}

// ensure the strconv/http imports stay used if a future edit drops a branch.
