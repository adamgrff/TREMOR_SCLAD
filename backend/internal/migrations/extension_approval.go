package migrations

import (
	_ "embed"
	"errors"
)

// This list is part of the reviewed executable, never an operator-supplied file
// or CLI/environment value. E1A intentionally approves no deployable migration.
// Releasing a new allowlist requires source/release review. Protecting the tool
// executable and its distribution is an external operational trust boundary.
//
//go:embed extension_approvals.json
var releaseExtensionApprovals []byte

var ErrExtensionUnapproved = errors.New("extension manifest is not approved by this tool release")

func releaseApproves(hash string) bool {
	var hashes []string
	if strictJSON(releaseExtensionApprovals, &hashes) != nil || hashes == nil {
		return false
	}
	return approvedHash(hash, hashes)
}
func approvedHash(hash string, hashes []string) bool {
	if !checksumPattern.MatchString(hash) {
		return false
	}
	seen := map[string]bool{}
	matched := false
	for _, approved := range hashes {
		if !checksumPattern.MatchString(approved) || seen[approved] {
			return false
		}
		seen[approved] = true
		matched = matched || approved == hash
	}
	return matched
}
