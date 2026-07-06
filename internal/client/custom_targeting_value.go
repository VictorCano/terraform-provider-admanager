package client

import (
	"context"
	"net/http"
)

// CustomTargetingValue mirrors the GoogleAdsAdmanagerV1__CustomTargetingValue
// resource from the discovery document (rev 20260701). Field names and JSON tags
// come straight from that schema.
//
// Reads only. The REST API exposes custom targeting values as read-only
// (networks.customTargetingValues has get and list, no create/update/delete as of
// rev 20260701). Writes go through the SOAP shim in internal/soap; see CLAUDE.md
// "SOAP shim for custom targeting values".
//
// Unlike CustomTargetingKey, this schema has NO customTargetingValueId field: the
// discovery doc does not define one for values (it exists only on
// EntitySignalsMapping). The numeric id is therefore parsed from the resource
// name (name) when the provider needs it, never read from a dedicated field.
type CustomTargetingValue struct {
	// Name is the resource name:
	// networks/{network_code}/customTargetingValues/{custom_targeting_value_id}.
	Name string `json:"name,omitempty"`

	// CustomTargetingKey is the resource name of the parent key. Required and
	// immutable: networks/{network_code}/customTargetingKeys/{custom_targeting_key_id}.
	CustomTargetingKey string `json:"customTargetingKey,omitempty"`

	// AdTagName is the value string used in ad tags (the SOAP field is "name").
	// Immutable and limited to 40 characters.
	AdTagName string `json:"adTagName,omitempty"`

	DisplayName string `json:"displayName,omitempty"` // Optional.

	// MatchType is Required and Immutable:
	// EXACT|BROAD|PREFIX|BROAD_PREFIX|SUFFIX|CONTAINS.
	MatchType string `json:"matchType,omitempty"`

	// Status is output-only: ACTIVE|INACTIVE. The lifecycle end state is INACTIVE
	// (deactivate); values have no archive or hard delete.
	Status string `json:"status,omitempty"`
}

// GetCustomTargetingValue fetches a single value by its full (flat) resource
// name. A missing value surfaces as an *APIError with StatusCode 404 (see
// IsNotFound). This is the read half of the hybrid resource; writes go through
// internal/soap.
func (c *Client) GetCustomTargetingValue(ctx context.Context, name string) (*CustomTargetingValue, error) {
	var out CustomTargetingValue
	if err := c.do(ctx, http.MethodGet, resourcePath(name), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
