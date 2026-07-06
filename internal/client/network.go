package client

import (
	"context"
	"net/http"
	"net/url"
)

// Network mirrors the GoogleAdsAdmanagerV1__Network resource. Every field is
// output-only in the API.
type Network struct {
	Name                   string   `json:"name"`
	NetworkCode            string   `json:"networkCode"`
	DisplayName            string   `json:"displayName"`
	TimeZone               string   `json:"timeZone"`
	CurrencyCode           string   `json:"currencyCode"`
	SecondaryCurrencyCodes []string `json:"secondaryCurrencyCodes"`
	EffectiveRootAdUnit    string   `json:"effectiveRootAdUnit"`
	NetworkID              string   `json:"networkId"`
	PropertyCode           string   `json:"propertyCode"`
	TestNetwork            bool     `json:"testNetwork"`
}

// GetNetwork fetches the network this client is configured for. It will back
// the admanager_network data source and is the natural credential smoke test
// once resources exist; Configure deliberately does not call it, so that
// plans do not spend API quota on a redundant read.
func (c *Client) GetNetwork(ctx context.Context) (*Network, error) {
	var n Network
	err := c.do(ctx, http.MethodGet, "/v1/networks/"+url.PathEscape(c.networkCode), nil, nil, &n)
	if err != nil {
		return nil, err
	}
	return &n, nil
}
