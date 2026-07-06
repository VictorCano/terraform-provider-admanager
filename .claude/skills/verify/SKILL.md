---
name: verify
description: Verify terraform-provider-admanager changes end-to-end by driving the built provider through OpenTofu with dev overrides.
---

# Verifying terraform-provider-admanager

## Build

```bash
go build -o terraform-provider-admanager .
```

## Drive through OpenTofu (dev override)

Create a scratch dir with `dev.tfrc`:

```hcl
provider_installation {
  dev_overrides {
    "VictorCano/admanager" = "/path/to/terraform-provider-admanager"
  }
  direct {}
}
```

and a `main.tf` with `required_providers { admanager = { source = "VictorCano/admanager" } }`.
Then, from the scratch dir (no `tofu init` needed with dev overrides):

```bash
export TF_CLI_CONFIG_FILE=$PWD/dev.tfrc
tofu validate -no-color
tofu providers schema -json | jq '.provider_schemas'
tofu plan -no-color
```

## Gotchas

- **Provider blocks are NOT validated unless some resource/data source in
  the config uses the provider.** With an empty config, typos and wrong
  types in `provider "admanager" {}` pass `validate` and `plan` silently
  (the provider node is pruned from the graph). Always include at least one
  data source (e.g. `data "admanager_network" "current"`) when verifying
  provider-block behavior, once one exists.
- Provider `Configure` (and thus real auth) only runs when a resource or
  data source is planned. Auth errors surface at plan time, not validate.
- Unit tests never need credentials (httptest only). Acceptance tests
  (`TF_ACC=1`) need `ADMANAGER_NETWORK_CODE` + Google credentials and must
  only target a GAM **test network** (`Network.testNetwork == true`).
- `tofu` is installed here; `terraform` is not. tfplugindocs downloads its
  own terraform binary when generating docs.
