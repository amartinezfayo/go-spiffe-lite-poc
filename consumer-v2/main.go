// Command v2consumer is the contrast case: an existing go-spiffe/v2 importer,
// pinned at the tip of main by pseudo-version (the operation that fails while
// go.mod carries the replace directive). It also shows that values built with
// the lite packages are usable wherever the v2 alias packages are expected.
package main

import (
	"fmt"
	"os"

	litebundle "github.com/spiffe/go-spiffe/lite/bundle/x509bundle"
	"github.com/spiffe/go-spiffe/lite/spiffeid"
	v2bundle "github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
)

func main() {
	td, err := spiffeid.TrustDomainFromString("example.org")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Type identity across modules: a bundle built with the lite package
	// satisfies the v2 Source interface with no conversion.
	var source v2bundle.Source = litebundle.New(td)
	bundle, err := source.GetX509BundleForTrustDomain(td)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("v2 alias API accepted a lite-built bundle for %s\n", bundle.TrustDomain())
}
