package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/VictorCano/terraform-provider-admanager/internal/client"
)

// configureDataSourceClient extracts the *client.Client the provider stored in
// resp.DataSourceData (see admanagerProvider.Configure). It centralizes the type
// assertion every data source shares. A nil providerData means the provider is
// not configured yet (e.g. during schema validation), which is not an error;
// the returned client is nil and Read is not called in that phase.
func configureDataSourceClient(providerData any, diags *diag.Diagnostics) *client.Client {
	if providerData == nil {
		return nil
	}
	c, ok := providerData.(*client.Client)
	if !ok {
		diags.AddError(
			"Unexpected data source configure type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", providerData),
		)
		return nil
	}
	return c
}
