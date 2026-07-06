package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// Placement mirrors the GoogleAdsAdmanagerV1__Placement resource from the
// discovery document (rev 20260701). A placement groups ad units so they can be
// targeted together. Field names and JSON tags come straight from that schema;
// comments flag which fields are output-only.
type Placement struct {
	// Name is the resource name: networks/{network_code}/placements/{placement_id}.
	Name string `json:"name,omitempty"`

	DisplayName string `json:"displayName,omitempty"` // Required.

	Description string `json:"description,omitempty"`

	// TargetedAdUnits are the resource names of the ad units in this placement:
	// networks/{network_code}/adUnits/{ad_unit_id}.
	TargetedAdUnits []string `json:"targetedAdUnits,omitempty"`

	// PlacementCode is output-only: a Google-assigned string that identifies the
	// placement for ad serving.
	PlacementCode string `json:"placementCode,omitempty"`

	// PlacementID is output-only and deprecated (the numeric id embedded in Name).
	PlacementID string `json:"placementId,omitempty"`

	Status     string `json:"status,omitempty"`     // Output only: ACTIVE|INACTIVE|ARCHIVED.
	UpdateTime string `json:"updateTime,omitempty"` // Output only (google-datetime).
}

// placementsPath is the collection path for the configured network.
func (c *Client) placementsPath() string {
	return "/v1/networks/" + url.PathEscape(c.networkCode) + "/placements"
}

// CreatePlacement creates a placement under the configured network. It returns
// the created resource as the API echoes it back (with computed fields set).
func (c *Client) CreatePlacement(ctx context.Context, p *Placement) (*Placement, error) {
	var out Placement
	if err := c.do(ctx, http.MethodPost, c.placementsPath(), nil, p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPlacement fetches a single placement by its full resource name. A missing
// placement surfaces as an *APIError with StatusCode 404 (see IsNotFound).
func (c *Client) GetPlacement(ctx context.Context, name string) (*Placement, error) {
	var out Placement
	if err := c.do(ctx, http.MethodGet, resourcePath(name), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PatchPlacement updates the fields named in updateMask and no others. The mask
// is a comma-joined list of changed Placement field names (API names, e.g.
// "displayName,targetedAdUnits"). Fields absent from the mask are left
// untouched even if present in p, so callers only need to set what changes.
func (c *Client) PatchPlacement(ctx context.Context, p *Placement, updateMask []string) (*Placement, error) {
	if len(updateMask) == 0 {
		// An empty mask would ask the API to replace every field; refuse it so a
		// caller bug cannot wipe unmanaged fields.
		return nil, errors.New("admanager client: PatchPlacement requires a non-empty update mask")
	}
	query := url.Values{"updateMask": {strings.Join(updateMask, ",")}}
	var out Placement
	if err := c.do(ctx, http.MethodPatch, resourcePath(p.Name), query, p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ArchivePlacement archives exactly one placement via placements:batchArchive.
// The resource name is the only element in the batch, so no unrelated entity
// can be swept in. Archiving is the destroy semantics for placements — the API
// has no hard delete.
func (c *Client) ArchivePlacement(ctx context.Context, name string) error {
	body := struct {
		Names []string `json:"names"`
	}{Names: []string{name}}
	return c.do(ctx, http.MethodPost, c.placementsPath()+":batchArchive", nil, body, nil)
}
