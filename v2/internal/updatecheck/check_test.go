package updatecheck

import "testing"

func TestCompareSemVersion(t *testing.T) {
	tags := []string{
		"v2.0.0",
		"v2.1.0",
		"v2.0.1",
		"v2.1.0-beta.1",
	}

	parsed := make([]semVersion, 0, len(tags))
	for _, tag := range tags {
		v, ok := parseSemVersion(tag)
		if !ok {
			t.Fatalf("expected %s to parse", tag)
		}
		parsed = append(parsed, v)
	}

	for i := 1; i < len(parsed); i++ {
		if compareSemVersion(parsed[i-1], parsed[i]) == 0 {
			t.Fatalf("expected versions to differ: %s vs %s", parsed[i-1].Raw, parsed[i].Raw)
		}
	}

	if compareSemVersion(parsed[3], parsed[1]) >= 0 {
		t.Fatalf("expected prerelease %s to be older than stable %s", parsed[3].Raw, parsed[1].Raw)
	}
}

func TestParseSemVersionRejectsInvalid(t *testing.T) {
	if _, ok := parseSemVersion("main"); ok {
		t.Fatal("expected invalid version to be rejected")
	}
}

// TestArchiveURLForTag was removed when the updater switched from source
// archive downloads to pre-built binaries from GitHub Releases. The helper
// it covered no longer exists; the test had been broken on main since the
// switch and was keeping `go test ./...` red for anyone who cloned fresh.
