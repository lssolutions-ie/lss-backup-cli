package cli

import (
	"encoding/json"
	"io"
)

// jsonNewEncoder is a test-only alias so api_unit_test.go can build a
// JSON string without re-importing encoding/json at top-level (which
// would conflict with api.go's import if we ever change it).
func jsonNewEncoder(w io.Writer) *json.Encoder { return json.NewEncoder(w) }
