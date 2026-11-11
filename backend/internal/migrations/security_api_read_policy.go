package migrations

import _ "embed"

// APIReadPolicyV1 is inert, versioned release SQL. No startup/runtime code applies
// it. Production approvals remain empty. TEST fixtures approve its manifest and
// resulting checkpoint independently before ApplyProtectedExtension.
//
//go:embed security_api_read_v1.sql
var APIReadPolicyV1 string
