package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/VictorCano/terraform-provider-admanager/internal/provider"
)

// version is overridden by goreleaser at release time via ldflags.
var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/VictorCano/admanager",
		Debug:   *debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
