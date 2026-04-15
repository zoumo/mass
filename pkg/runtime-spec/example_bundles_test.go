package runtimespec_test

import (
	"io/fs"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	runtimespec "github.com/zoumo/mass/pkg/runtime-spec"
)

func TestExampleBundlesAreValid(t *testing.T) {
	bundlesRoot := filepath.Join("testdata", "bundles")

	var bundleDirs []string
	err := filepath.WalkDir(bundlesRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "config.json" {
			return nil
		}
		bundleDirs = append(bundleDirs, filepath.Dir(path))
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, bundleDirs, "expected checked-in example bundles under %s", bundlesRoot)

	sort.Strings(bundleDirs)
	for _, bundleDir := range bundleDirs {
		t.Run(filepath.Base(bundleDir), func(t *testing.T) {
			cfg, err := runtimespec.ParseConfig(bundleDir)
			require.NoError(t, err)
			require.NoError(t, runtimespec.ValidateConfig(cfg))
		})
	}
}
