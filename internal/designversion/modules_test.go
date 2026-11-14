// This file tests finding the modules a builder's go.mod lacks for a design.
package designversion

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/module"
)

// TestMissingModulesNamesTheSavedVersionOfEachModuleTheBuilderLacks compares
// the go.mod a revision saved with a builder's go.mod for a design's imports
// and checks the modules named, each at the saved version.
func TestMissingModulesNamesTheSavedVersionOfEachModuleTheBuilderLacks(t *testing.T) {
	const (
		goa      = "goa.design/goa/v3"
		usesGoa  = "package design\n\nimport . \"goa.design/goa/v3/dsl\"\n\nvar _ = API(\"shop\", nil)\n"
		savedGoa = "module example.com/shop\n\ngo 1.27.3\n\nrequire goa.design/goa/v3 v3.35.0\n"
	)
	cases := []struct {
		name    string
		design  map[string]string
		saved   string
		builder string
		want    []module.Version
	}{
		{
			name: "missing",
			design: map[string]string{
				"design/design.go":    usesGoa,
				"design/types.go":     "package design\n\nimport \"example.com/shop/design/sub\"\n\nvar _ = sub.T\n",
				"design/sub/types.go": "package sub\n\nvar T = 1\n",
			},
			saved:   savedGoa,
			builder: "module example.com/shop\n\ngo 1.27.3\n",
			want:    []module.Version{{Path: goa, Version: "v3.35.0"}},
		},
		{
			name:    "older",
			design:  map[string]string{"design/design.go": usesGoa},
			saved:   savedGoa,
			builder: "module example.com/shop\n\ngo 1.27.3\n\nrequire goa.design/goa/v3 v3.30.0\n",
			want:    []module.Version{{Path: goa, Version: "v3.35.0"}},
		},
		{
			name:    "newer",
			design:  map[string]string{"design/design.go": usesGoa},
			saved:   savedGoa,
			builder: "module example.com/shop\n\ngo 1.27.3\n\nrequire goa.design/goa/v3 v3.36.0\n",
		},
		{
			name:    "standard library",
			design:  map[string]string{"design/design.go": "package design\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n)\n\nvar _, _ = fmt.Sprint, http.Get\n"},
			saved:   savedGoa,
			builder: "module example.com/shop\n\ngo 1.27.3\n",
		},
		{
			name:   "longest module prefix",
			design: map[string]string{"design/design.go": "package design\n\nimport \"example.com/kit/telemetry/trace\"\n\nvar _ = trace.Start\n"},
			saved: "module example.com/shop\n\ngo 1.27.3\n\nrequire (\n" +
				"\texample.com/kit v1.2.0\n\texample.com/kit/telemetry v0.3.0\n)\n",
			builder: "module example.com/shop\n\ngo 1.27.3\n\nrequire example.com/kit v1.2.0\n",
			want:    []module.Version{{Path: "example.com/kit/telemetry", Version: "v0.3.0"}},
		},
		{
			name:   "required twice, at the higher version",
			design: map[string]string{"design/design.go": usesGoa},
			saved:  savedGoa,
			builder: "module example.com/shop\n\ngo 1.27.3\n\n" +
				"require goa.design/goa/v3 v3.35.0\n\nrequire goa.design/goa/v3 v3.30.0\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved, err := Requirements(tc.saved)
			require.NoError(t, err)
			builder, err := Requirements(tc.builder)
			require.NoError(t, err)

			got, err := MissingModules(tc.design, saved, builder)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
