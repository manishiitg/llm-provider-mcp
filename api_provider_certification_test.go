package llmproviders

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The API-transport equivalent of TestActiveCodingAgentProvidersSatisfyP0Contract.
// A provider may not claim a capability it cannot demonstrate against the real
// provider API, and a gap must be named in knownAPICertificationGaps rather
// than simply absent.
func TestActiveAPIProvidersSatisfyP0Contract(t *testing.T) {
	for _, contract := range APIProviderContracts() {
		missing := MissingP0APICertifications(contract)

		allowed := map[APIProviderCertificationID]struct{}{}
		for _, id := range knownAPICertificationGaps[contract.Provider] {
			allowed[id] = struct{}{}
		}

		var unexpected []APIProviderCertificationID
		for _, id := range missing {
			if _, ok := allowed[id]; !ok {
				unexpected = append(unexpected, id)
			}
		}
		if len(unexpected) > 0 {
			t.Errorf("%s is missing release-blocking P0 API certifications: %v. Write the proof, or add the IDs to knownAPICertificationGaps with a reason.",
				contract.Provider, unexpected)
		}

		// A stale allowance is its own defect: it means somebody wrote the
		// proof but left the gap listed, so the ledger stops reflecting
		// reality and the next reader trusts it anyway.
		missingSet := map[APIProviderCertificationID]struct{}{}
		for _, id := range missing {
			missingSet[id] = struct{}{}
		}
		for _, id := range knownAPICertificationGaps[contract.Provider] {
			if _, stillMissing := missingSet[id]; !stillMissing {
				t.Errorf("%s knownAPICertificationGaps still lists %s but a proof is now registered — remove the allowance",
					contract.Provider, id)
			}
		}
	}
}

// Every registered proof must name a test that actually exists. A certification
// pointing at a deleted or renamed test is worse than no certification: it
// reports coverage that cannot run.
func TestAPIProviderCertificationsReferenceExistingTests(t *testing.T) {
	for _, contract := range APIProviderContracts() {
		certs := APIProviderCertifications(contract.Provider)
		seen := map[APIProviderCertificationID]string{}
		for _, cert := range certs {
			if cert.ID == "" {
				t.Fatalf("%s has a certification with an empty id: %#v", contract.Provider, cert)
			}
			if previous := seen[cert.ID]; previous != "" {
				t.Fatalf("%s certification %s registered twice: %s and %s",
					contract.Provider, cert.ID, previous, cert.TestName)
			}
			seen[cert.ID] = cert.TestName

			if strings.TrimSpace(cert.TestFile) == "" || strings.TrimSpace(cert.TestName) == "" {
				t.Fatalf("%s certification %s must name a test file and test function: %#v",
					contract.Provider, cert.ID, cert)
			}
			raw, err := os.ReadFile(filepath.Clean(cert.TestFile))
			if err != nil {
				t.Fatalf("%s certification %s references unreadable test file %s: %v",
					contract.Provider, cert.ID, cert.TestFile, err)
			}
			if !strings.Contains(string(raw), "func "+cert.TestName+"(") {
				t.Fatalf("%s certification %s references missing test %s in %s",
					contract.Provider, cert.ID, cert.TestName, cert.TestFile)
			}
		}
	}
}

// P0 proofs must be live. A deterministic fixture cannot certify that a real
// credential is rejected correctly, that a real 429 is classified as rate
// limiting, or that a real usage payload reaches the cost ledger — those are
// exactly the properties that only fail against the actual provider.
func TestP0APICertificationsAreRealE2EAndGated(t *testing.T) {
	for _, contract := range APIProviderContracts() {
		byID := map[APIProviderCertificationID]APIProviderCertification{}
		for _, cert := range APIProviderCertifications(contract.Provider) {
			byID[cert.ID] = cert
		}
		for _, id := range RequiredP0APICertificationIDs(contract) {
			cert, ok := byID[id]
			if !ok {
				continue // absence is TestActiveAPIProvidersSatisfyP0Contract's job
			}
			if !cert.RealE2E {
				t.Errorf("%s P0 certification %s must be backed by a real provider E2E: %#v",
					contract.Provider, id, cert)
			}
			if len(cert.Env) == 0 {
				t.Errorf("%s P0 certification %s must declare its live gate env var so it can actually be run",
					contract.Provider, id)
			}
		}
	}
}
