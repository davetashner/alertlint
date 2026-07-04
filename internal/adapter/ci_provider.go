package adapter

import (
	"iter"

	"github.com/davetashner/alertlint/internal/identity"
)

// CIProvider is the fourth narrow interface: a source of the normalized
// CMDB CI inventory the resolver joins against (REQ-ID-001).
//
// This resolves the open question in docs/specs/identity-resolution.md
// ("CI inventory access shape"): a dedicated interface rather than a
// special-cased ServiceNow adapter extension, because it keeps capability
// discovery uniform (a vendor module satisfies CIProvider or it doesn't),
// lets tests inject inventories through the same registry path as every
// other data class, and leaves room for a non-ServiceNow CMDB later
// without touching the resolver. The ServiceNow vendor module is expected
// to be the v1 implementer.
//
// The window parameter exists for cache-key symmetry (like ConfigProvider):
// the inventory is a snapshot at pull time.
type CIProvider interface {
	Provider
	FetchCIs(scope Scope, window TimeWindow) iter.Seq2[identity.CI, error]
}
