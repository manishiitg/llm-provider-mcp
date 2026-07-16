package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	llmproviders "github.com/manishiitg/multi-llm-provider-go"
)

func main() {
	providerFlag := flag.String("provider", "", "coding CLI provider ID")
	packageFlag := flag.String("package", "", "repository-relative Go package containing the P0 tests")
	flag.Parse()

	provider := llmproviders.Provider(strings.TrimSpace(*providerFlag))
	expectedPackage := filepath.ToSlash(strings.Trim(strings.TrimSpace(*packageFlag), "/"))
	if provider == "" || expectedPackage == "" {
		fmt.Fprintln(os.Stderr, "-provider and -package are required")
		os.Exit(2)
	}
	contract, ok := llmproviders.GetCodingAgentProviderContract(provider, "")
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown coding CLI provider %q\n", provider)
		os.Exit(2)
	}

	requiredIDs := llmproviders.RequiredP0CodingAgentCertificationIDs(contract)
	required := make(map[llmproviders.CodingAgentCertificationID]struct{}, len(requiredIDs))
	for _, id := range requiredIDs {
		required[id] = struct{}{}
	}
	found := make(map[llmproviders.CodingAgentCertificationID]struct{}, len(requiredIDs))
	testNames := make(map[string]struct{}, len(requiredIDs))
	for _, certification := range llmproviders.CodingAgentProviderCertifications(provider) {
		if _, needed := required[certification.ID]; !needed {
			continue
		}
		if !certification.RealE2E {
			fmt.Fprintf(os.Stderr, "%s P0 certification %s is not a real E2E\n", provider, certification.ID)
			os.Exit(1)
		}
		testPackage := filepath.ToSlash(filepath.Dir(certification.TestFile))
		if testPackage != expectedPackage {
			fmt.Fprintf(os.Stderr, "%s P0 test %s is in %s, runner expected %s\n", provider, certification.TestName, testPackage, expectedPackage)
			os.Exit(1)
		}
		found[certification.ID] = struct{}{}
		testNames[certification.TestName] = struct{}{}
	}
	for _, id := range requiredIDs {
		if _, ok := found[id]; !ok {
			fmt.Fprintf(os.Stderr, "%s is missing required P0 test %s\n", provider, id)
			os.Exit(1)
		}
	}

	names := make([]string, 0, len(testNames))
	for name := range testNames {
		names = append(names, regexp.QuoteMeta(name))
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Fprintf(os.Stderr, "%s has no registered P0 tests\n", provider)
		os.Exit(1)
	}
	fmt.Printf("^(%s)$\n", strings.Join(names, "|"))
}
