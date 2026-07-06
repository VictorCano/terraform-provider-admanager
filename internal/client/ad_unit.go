package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/time/rate"
)

// AdUnit mirrors the GoogleAdsAdmanagerV1__AdUnit resource from the discovery
// document (rev 20260701). Field names and JSON tags come straight from that
// schema; comments flag which fields are output-only (the API rejects or
// ignores them on write) and which are immutable.
//
// Booleans that the API treats as tri-state on write use *bool so the provider
// can distinguish "leave unset / inherit" (nil) from an explicit true/false.
// Output-only booleans use a plain bool since they are only ever decoded.
type AdUnit struct {
	// Name is the resource name: networks/{network_code}/adUnits/{ad_unit_id}.
	Name string `json:"name,omitempty"`

	DisplayName string `json:"displayName,omitempty"` // Required.

	// ParentAdUnit is required and immutable. Format:
	// networks/{network_code}/adUnits/{ad_unit_id}.
	ParentAdUnit string `json:"parentAdUnit,omitempty"`

	// AdUnitCode is optional and immutable; the API assigns one when omitted.
	AdUnitCode string `json:"adUnitCode,omitempty"`

	// AdUnitID is output-only and deprecated (the numeric id embedded in Name).
	AdUnitID string `json:"adUnitId,omitempty"`

	Description string `json:"description,omitempty"`

	// AppliedTargetWindow is settable (TOP|BLANK); EffectiveTargetWindow is the
	// inherited/output-only counterpart.
	AppliedTargetWindow   string `json:"appliedTargetWindow,omitempty"`
	EffectiveTargetWindow string `json:"effectiveTargetWindow,omitempty"` // Output only.

	ExplicitlyTargeted *bool `json:"explicitlyTargeted,omitempty"`

	// AppliedAdsenseEnabled is the value set directly on this ad unit; nil means
	// "inherit". EffectiveAdsenseEnabled is the resolved, output-only value.
	AppliedAdsenseEnabled   *bool `json:"appliedAdsenseEnabled,omitempty"`
	EffectiveAdsenseEnabled bool  `json:"effectiveAdsenseEnabled,omitempty"` // Output only.

	SmartSizeMode string `json:"smartSizeMode,omitempty"`

	// RefreshDelay is a google-duration string (e.g. "30s"); mobile apps only.
	RefreshDelay string `json:"refreshDelay,omitempty"`

	// ExternalSetTopBoxChannelID is deprecated in the API but still settable.
	ExternalSetTopBoxChannelID string `json:"externalSetTopBoxChannelId,omitempty"`

	Status      string `json:"status,omitempty"`      // Output only: ACTIVE|INACTIVE|ARCHIVED.
	HasChildren bool   `json:"hasChildren,omitempty"` // Output only.
	UpdateTime  string `json:"updateTime,omitempty"`  // Output only (google-datetime).

	// ParentPath is the output-only chain from the root to this unit's parent.
	ParentPath []AdUnitParent `json:"parentPath,omitempty"`

	AdUnitSizes []AdUnitSize `json:"adUnitSizes,omitempty"`

	AppliedTeams []string `json:"appliedTeams,omitempty"`
	Teams        []string `json:"teams,omitempty"` // Output only.

	// Label and frequency-cap fields are part of the API resource and are kept
	// here so the client is a faithful mirror. The Terraform resource does not
	// surface them yet (see the resource's deferral note).
	AppliedLabels               []AppliedLabel      `json:"appliedLabels,omitempty"`
	EffectiveAppliedLabels      []AppliedLabel      `json:"effectiveAppliedLabels,omitempty"`      // Output only.
	AppliedLabelFrequencyCaps   []LabelFrequencyCap `json:"appliedLabelFrequencyCaps,omitempty"`   //
	EffectiveLabelFrequencyCaps []LabelFrequencyCap `json:"effectiveLabelFrequencyCaps,omitempty"` // Output only.
}

// AdUnitParent is an output-only summary of an ancestor ad unit.
type AdUnitParent struct {
	ParentAdUnit string `json:"parentAdUnit,omitempty"`
	AdUnitCode   string `json:"adUnitCode,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
}

// AdUnitSize is one servable size (plus environment and companions) of an ad
// unit; it mirrors GoogleAdsAdmanagerV1__AdUnitSize.
type AdUnitSize struct {
	Size Size `json:"size"`
	// Companions are only valid when EnvironmentType is VIDEO_PLAYER.
	Companions      []Size `json:"companions,omitempty"`
	EnvironmentType string `json:"environmentType,omitempty"` // BROWSER|VIDEO_PLAYER.
}

// Size mirrors GoogleAdsAdmanagerV1__Size. Width and Height are int32 in the
// API; they are modeled as int64 here to line up with the Terraform Int64 type
// (the JSON wire form is identical).
type Size struct {
	Width    int64  `json:"width,omitempty"`
	Height   int64  `json:"height,omitempty"`
	SizeType string `json:"sizeType,omitempty"` // PIXEL|ASPECT_RATIO|INTERSTITIAL|IGNORED|NATIVE|FLUID|AUDIO.
}

// AppliedLabel mirrors GoogleAdsAdmanagerV1__AppliedLabel.
type AppliedLabel struct {
	Label   string `json:"label,omitempty"` // networks/{network_code}/labels/{label_id}.
	Negated bool   `json:"negated,omitempty"`
}

// LabelFrequencyCap mirrors GoogleAdsAdmanagerV1__LabelFrequencyCap.
type LabelFrequencyCap struct {
	Label        string        `json:"label,omitempty"`
	FrequencyCap *FrequencyCap `json:"frequencyCap,omitempty"`
}

// FrequencyCap mirrors GoogleAdsAdmanagerV1__FrequencyCap. MaxImpressions and
// TimeAmount are int64 values that the API encodes as JSON strings.
type FrequencyCap struct {
	MaxImpressions string `json:"maxImpressions,omitempty"`
	TimeUnit       string `json:"timeUnit,omitempty"`
	TimeAmount     string `json:"timeAmount,omitempty"`
}

// adUnitsPath is the collection path for the configured network.
func (c *Client) adUnitsPath() string {
	return "/v1/networks/" + url.PathEscape(c.networkCode) + "/adUnits"
}

// resourcePath turns a full resource name (networks/.../adUnits/{id}) into an
// API path. The slashes are real path separators and must not be escaped.
func resourcePath(name string) string {
	return "/v1/" + name
}

// CreateAdUnit creates an ad unit under the configured network. The parent ad
// unit is carried in au.ParentAdUnit, not the URL. It returns the created
// resource as the API echoes it back (with all computed fields populated).
func (c *Client) CreateAdUnit(ctx context.Context, au *AdUnit) (*AdUnit, error) {
	var out AdUnit
	if err := c.do(ctx, http.MethodPost, c.adUnitsPath(), nil, au, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListAdUnitsOptions carries the optional query parameters for ListAdUnits.
// The zero value lists every ad unit in the network.
type ListAdUnitsOptions struct {
	// Filter is passed through verbatim to the API `filter` query parameter
	// (AIP-160 filter syntax; see
	// https://developers.google.com/ad-manager/api/beta/filters). Empty means no
	// filter.
	Filter string

	// OrderBy is passed through to the API `orderBy` query parameter. Empty means
	// the API's default ordering.
	OrderBy string

	// PageSize sets the per-request `pageSize`. Zero uses the API default (50).
	// Larger values (up to the API max of 1000) mean fewer round trips through
	// the rate limiter for large result sets.
	PageSize int
}

// listAdUnitsResponse mirrors GoogleAdsAdmanagerV1__ListAdUnitsResponse. Only
// the fields the provider consumes are decoded; totalSize is intentionally
// omitted because the API does not populate it unless a field mask requests it.
type listAdUnitsResponse struct {
	AdUnits       []AdUnit `json:"adUnits"`
	NextPageToken string   `json:"nextPageToken"`
}

// ListAdUnits returns every ad unit in the configured network that matches
// opts, following nextPageToken across all pages. Each page is a separate GET
// that goes through do (and therefore the rate limiter). To guard against an
// unbounded loop from a too-broad filter, it stops after maxListPages pages and
// returns an error rather than silently truncating.
func (c *Client) ListAdUnits(ctx context.Context, opts ListAdUnitsOptions) ([]AdUnit, error) {
	var out []AdUnit
	pageToken := ""
	for page := 1; ; page++ {
		query := url.Values{}
		if opts.Filter != "" {
			query.Set("filter", opts.Filter)
		}
		if opts.OrderBy != "" {
			query.Set("orderBy", opts.OrderBy)
		}
		if opts.PageSize > 0 {
			query.Set("pageSize", strconv.Itoa(opts.PageSize))
		}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}

		var resp listAdUnitsResponse
		if err := c.do(ctx, http.MethodGet, c.adUnitsPath(), query, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.AdUnits...)

		if resp.NextPageToken == "" {
			return out, nil
		}
		pageToken = resp.NextPageToken
		if page >= c.maxListPages {
			return nil, fmt.Errorf(
				"admanager client: listing ad units stopped at the %d-page safety cap; the filter matched too many ad units — narrow the filter (https://developers.google.com/ad-manager/api/beta/filters) and try again",
				c.maxListPages)
		}
	}
}

// GetAdUnit fetches a single ad unit by its full resource name. A missing ad
// unit surfaces as an *APIError with StatusCode 404 (see IsNotFound).
func (c *Client) GetAdUnit(ctx context.Context, name string) (*AdUnit, error) {
	var out AdUnit
	if err := c.do(ctx, http.MethodGet, resourcePath(name), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PatchAdUnit updates the fields named in updateMask and no others. The mask is
// the API field-mask: a comma-joined list of the changed AdUnit field names
// (e.g. "displayName,adUnitSizes"). Fields absent from the mask are left
// untouched even if present in au, so callers only need to set what changes.
func (c *Client) PatchAdUnit(ctx context.Context, au *AdUnit, updateMask []string) (*AdUnit, error) {
	if len(updateMask) == 0 {
		// An empty mask would ask the API to replace every field; refuse it so a
		// caller bug cannot wipe unmanaged fields.
		return nil, errors.New("admanager client: PatchAdUnit requires a non-empty update mask")
	}
	query := url.Values{"updateMask": {strings.Join(updateMask, ",")}}
	var out AdUnit
	if err := c.do(ctx, http.MethodPatch, resourcePath(au.Name), query, au, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ArchiveAdUnit archives exactly one ad unit via adUnits:batchArchive. The
// resource name is the only element in the batch, so no unrelated entity can be
// swept in. Archiving is the destroy semantics for ad units — the API has no
// hard delete.
func (c *Client) ArchiveAdUnit(ctx context.Context, name string) error {
	body := struct {
		Names []string `json:"names"`
	}{Names: []string{name}}
	return c.do(ctx, http.MethodPost, c.adUnitsPath()+":batchArchive", nil, body, nil)
}

// IsNotFound reports whether err is an *APIError carrying HTTP 404. Read uses
// it to remove a resource from state when it has disappeared from Ad Manager.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// NetworkCode returns the network code the client is scoped to. Import uses it
// to expand a bare numeric ad unit id into a full resource name.
func (c *Client) NetworkCode() string {
	return c.networkCode
}

// HTTPClient returns the oauth2-authenticated HTTP client. The SOAP shim
// (internal/soap) reuses it so custom targeting *value* writes carry the same
// credentials as REST reads instead of building a parallel authenticated client.
func (c *Client) HTTPClient() *http.Client {
	return c.httpClient
}

// Limiter returns the shared client-side token bucket. The SOAP shim waits on it
// before every call so REST and SOAP traffic draw from one rate budget; nothing
// may bypass it.
func (c *Client) Limiter() *rate.Limiter {
	return c.limiter
}

// UserAgent returns the configured user agent
// ("terraform-provider-admanager/<version>"), reused verbatim as the SOAP
// applicationName in the RequestHeader.
func (c *Client) UserAgent() string {
	return c.userAgent
}
