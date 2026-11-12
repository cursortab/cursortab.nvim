package scenario

import (
	"path/filepath"
	"strings"
	"testing"

	"cursortab/assert"
)

// TestScenarios runs every behavior scenario in testdata and checks its
// expect steps.
func TestScenarios(t *testing.T) {
	paths, err := filepath.Glob("testdata/*.txtar")
	assert.NoError(t, err, "glob")
	assert.True(t, len(paths) > 0, "no scenarios in testdata")
	for _, path := range paths {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".txtar"), func(t *testing.T) {
			t.Parallel()
			scs, err := LoadScenario(path, nil)
			assert.NoError(t, err, "load")
			for _, sc := range scs {
				for _, to := range Run(sc, Config{}).Targets {
					assert.NoError(t, to.Error, "run")
					for _, f := range to.Failures() {
						t.Error(f)
					}
				}
			}
		})
	}
}
