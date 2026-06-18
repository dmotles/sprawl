package merge

import (
	"os"
	"testing"
)

// TestMain scrubs repo-scoping GIT_* vars from the test process environment so
// the integration tests spawn git hermetically against their own temp repos.
// Without this, a leaked GIT_DIR — e.g. exported by git into the QUM-808
// pre-commit hook that runs `make validate` — would point nested git at the
// outer repo and break the suite (QUM-836).
func TestMain(m *testing.M) {
	for _, v := range []string{
		"GIT_DIR", "GIT_INDEX_FILE", "GIT_WORK_TREE",
		"GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_PREFIX",
	} {
		os.Unsetenv(v)
	}
	os.Exit(m.Run())
}
