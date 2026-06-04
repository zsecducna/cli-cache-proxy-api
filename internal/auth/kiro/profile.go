package kiro

import (
	"fmt"
	"strings"
)

// RequireProfileArn rejects Kiro credentials that cannot identify the
// CodeWhisperer profile required by the runtime generate endpoint.
func RequireProfileArn(profileArn string, operation string) error {
	if strings.TrimSpace(profileArn) != "" {
		return nil
	}
	op := strings.TrimSpace(operation)
	if op == "" {
		op = "kiro"
	}
	return fmt.Errorf("%s: missing profile ARN; re-run Kiro login so the credential can resolve and persist profile_arn", op)
}
