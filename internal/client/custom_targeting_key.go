package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// CustomTargetingKey mirrors the GoogleAdsAdmanagerV1__CustomTargetingKey
// resource from the discovery document (rev 20260701). Field names and JSON
// tags come straight from that schema; comments flag output-only and immutable
// fields.
//
// The API field "type" is a Go keyword and cannot name a struct field, so the
// exported field is Type with a `json:"type"` tag — a faithful wire mapping
// with a clean identifier.
type CustomTargetingKey struct {
	// Name is the resource name:
	// networks/{network_code}/customTargetingKeys/{custom_targeting_key_id}.
	Name string `json:"name,omitempty"`

	// AdTagName is the key name used in ad tags. It is immutable and limited to
	// 10 characters.
	AdTagName string `json:"adTagName,omitempty"`

	DisplayName string `json:"displayName,omitempty"` // Optional.

	// Type is Required: PREDEFINED (fixed set of values) or FREEFORM.
	Type string `json:"type,omitempty"`

	// ReportableType is Required: OFF|ON|CUSTOM_DIMENSION.
	ReportableType string `json:"reportableType,omitempty"`

	// CustomTargetingKeyID is output-only and deprecated (the numeric id embedded
	// in Name).
	CustomTargetingKeyID string `json:"customTargetingKeyId,omitempty"`

	// Status is output-only: ACTIVE|INACTIVE. There is no ARCHIVED state for
	// custom targeting keys; the lifecycle end state is INACTIVE (deactivate).
	Status string `json:"status,omitempty"`
}

// customTargetingKeysPath is the collection path for the configured network.
func (c *Client) customTargetingKeysPath() string {
	return "/v1/networks/" + url.PathEscape(c.networkCode) + "/customTargetingKeys"
}

// CreateCustomTargetingKey creates a custom targeting key under the configured
// network. It returns the created resource as the API echoes it back (with
// computed fields populated).
func (c *Client) CreateCustomTargetingKey(ctx context.Context, k *CustomTargetingKey) (*CustomTargetingKey, error) {
	var out CustomTargetingKey
	if err := c.do(ctx, http.MethodPost, c.customTargetingKeysPath(), nil, k, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCustomTargetingKey fetches a single key by its full resource name. A
// missing key surfaces as an *APIError with StatusCode 404 (see IsNotFound).
func (c *Client) GetCustomTargetingKey(ctx context.Context, name string) (*CustomTargetingKey, error) {
	var out CustomTargetingKey
	if err := c.do(ctx, http.MethodGet, resourcePath(name), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PatchCustomTargetingKey updates the fields named in updateMask and no others.
// The mask is a comma-joined list of changed field names (API names, e.g.
// "displayName,reportableType"). Fields absent from the mask are left untouched
// even if present in k. The immutable adTagName is never patched.
func (c *Client) PatchCustomTargetingKey(ctx context.Context, k *CustomTargetingKey, updateMask []string) (*CustomTargetingKey, error) {
	if len(updateMask) == 0 {
		// An empty mask would ask the API to replace every field; refuse it so a
		// caller bug cannot wipe unmanaged fields.
		return nil, errors.New("admanager client: PatchCustomTargetingKey requires a non-empty update mask")
	}
	query := url.Values{"updateMask": {strings.Join(updateMask, ",")}}
	var out CustomTargetingKey
	if err := c.do(ctx, http.MethodPatch, resourcePath(k.Name), query, k, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeactivateCustomTargetingKey deactivates exactly one key via
// customTargetingKeys:batchDeactivate. The resource name is the only element in
// the batch, so no unrelated entity can be swept in. Deactivation (end state
// INACTIVE) is the destroy semantics for custom targeting keys — the API has no
// archive or hard delete for them.
func (c *Client) DeactivateCustomTargetingKey(ctx context.Context, name string) error {
	body := struct {
		Names []string `json:"names"`
	}{Names: []string{name}}
	return c.do(ctx, http.MethodPost, c.customTargetingKeysPath()+":batchDeactivate", nil, body, nil)
}
