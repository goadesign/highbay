// This file tests required modules and effective replacements for a design.
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
			name:    "prefix by path element",
			design:  map[string]string{"design/design.go": "package design\n\nimport \"example.com/kitchen/x\"\n\nvar _ = x.Y\n"},
			saved:   "module example.com/shop\n\ngo 1.27.3\n\nrequire example.com/kit v1.2.0\n",
			builder: "module example.com/shop\n\ngo 1.27.3\n",
		},
		{
			name:    "own module under a required module's path",
			design:  map[string]string{"design/design.go": "package design\n\nimport \"example.com/org/shop/design/sub\"\n\nvar _ = sub.T\n"},
			saved:   "module example.com/org/shop\n\ngo 1.27.3\n\nrequire example.com/org v1.3.0\n",
			builder: "module example.com/org/shop\n\ngo 1.27.3\n\nrequire example.com/org v1.2.0\n",
		},
		{
			name:   "required three times, at the highest version",
			design: map[string]string{"design/design.go": usesGoa},
			saved:  savedGoa,
			builder: "module example.com/shop\n\ngo 1.27.3\n\n" +
				"require goa.design/goa/v3 v3.30.0\n\nrequire goa.design/goa/v3 v3.35.0\n\nrequire goa.design/goa/v3 v3.32.0\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved, err := Requirements(tc.saved)
			require.NoError(t, err)
			builder, err := Requirements(tc.builder)
			require.NoError(t, err)

			got, findings, err := MissingModules(tc.design, saved, builder)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Empty(t, findings)
		})
	}
}

// TestMissingModulesComparesEffectiveReplacements exercises Go's selectors at
// the builder's resulting requirement, while preserving unrelated directives
// and quoting original source only when an imported module's target differs.
func TestMissingModulesComparesEffectiveReplacements(t *testing.T) {
	const (
		mod       = "example.com/kit"
		target    = "example.com/fork v1.4.0"
		wildcard  = "replace example.com/kit => " + target + "\n"
		exact     = "replace example.com/kit v1.2.0 => " + target + "\n"
		other     = "replace example.com/kit => example.com/other v1.4.0\n"
		blockLine = "\t\"example.com/kit\"   \"v1.2.0\" => \"example.com/fork\" \"v1.4.0\" // keep spelling"
	)
	cases := []struct {
		name           string
		savedReplace   string
		builderReplace string
		builderVersion string
		missing        bool
		finding        []string
	}{
		{
			name: "missing saved wildcard", savedReplace: wildcard,
			builderVersion: "v1.2.0",
			finding: []string{wildcard, "no effective replacement for example.com/kit@v1.2.0",
				"Apply an equivalent replacement target example.com/fork@v1.4.0 to example.com/kit@v1.2.0"},
		},
		{
			name: "missing requirement already has correct wildcard", savedReplace: exact,
			builderReplace: wildcard, missing: true,
		},
		{
			name: "old exact replacement stops applying after upgrade", savedReplace: exact,
			builderVersion: "v1.1.0", missing: true,
			builderReplace: "replace example.com/kit v1.1.0 => " + target + "\n",
			finding:        []string{exact, "no effective replacement for example.com/kit@v1.2.0"},
		},
		{
			name: "saved selector must apply at newer builder version", savedReplace: exact,
			builderVersion: "v1.3.0", builderReplace: exact,
			finding: []string{exact, "no effective replacement for example.com/kit@v1.3.0",
				"to example.com/kit@v1.3.0, the builder's selected requirement"},
		},
		{
			name: "newer exact selector matches saved wildcard", savedReplace: wildcard,
			builderVersion: "v1.3.0",
			builderReplace: "replace example.com/kit v1.3.0 => " + target + "\n",
		},
		{
			name: "wildcard matches saved exact selector", savedReplace: exact,
			builderVersion: "v1.2.0", builderReplace: wildcard,
		},
		{
			name: "saved exact wins over earlier wildcard", savedReplace: other + exact,
			builderVersion: "v1.2.0", builderReplace: wildcard,
		},
		{
			name: "saved exact wins over later wildcard", savedReplace: exact + other,
			builderVersion: "v1.2.0", builderReplace: wildcard,
		},
		{
			name: "builder exact wins over wildcard", savedReplace: wildcard,
			builderVersion: "v1.2.0", builderReplace: wildcard +
				"replace example.com/kit v1.2.0 => example.com/other v1.4.0\n",
			finding: []string{wildcard, "Builder go.mod applies a different replacement",
				"replace example.com/kit v1.2.0 => example.com/other v1.4.0"},
		},
		{
			name: "other version selectors do not apply", builderVersion: "v1.3.0",
			savedReplace:   "replace example.com/kit v1.0.0 => " + target + "\n",
			builderReplace: exact,
		},
		{
			name: "unrelated replacement is preserved", builderVersion: "v1.2.0",
			builderReplace: "replace example.com/unused => example.com/other v1.4.0\n",
		},
		{
			name: "unexpected effective builder replacement", builderVersion: "v1.3.0",
			builderReplace: wildcard,
			finding: []string{"applies this replacement to example.com/kit@v1.3.0",
				"saved go.mod has no effective replacement for example.com/kit@v1.2.0", wildcard},
		},
		{
			name: "same path different target version", savedReplace: wildcard,
			builderVersion: "v1.2.0",
			builderReplace: "replace example.com/kit => example.com/fork v1.5.0\n",
			finding:        []string{wildcard, "example.com/fork v1.5.0"},
		},
		{
			name: "different target path", savedReplace: wildcard,
			builderVersion: "v1.2.0", builderReplace: other,
			finding: []string{wildcard, other},
		},
		{
			name: "builder local replacement differs", savedReplace: wildcard,
			builderVersion: "v1.2.0", builderReplace: "replace example.com/kit => ../local\n",
			finding: []string{wildcard, "replace example.com/kit => ../local"},
		},
		{
			name:           "quoted block spelling is semantically equivalent",
			savedReplace:   "replace (\n" + blockLine + "\n)\n",
			builderVersion: "v1.2.0", builderReplace: wildcard,
		},
		{
			name:           "quoted block entry diagnostic is verbatim",
			savedReplace:   "replace (\n" + blockLine + "\n)\n",
			builderVersion: "v1.3.0", builderReplace: other,
			finding: []string{"Replacement entry inside a replace block:\n" + blockLine + "\n",
				"to example.com/kit@v1.3.0, the builder's selected requirement"},
		},
		{
			name:           "standalone source spacing and comment survive",
			savedReplace:   "  replace \"example.com/kit\"  => \"example.com/fork\" v1.4.0 // source\n",
			builderVersion: "v1.2.0",
			finding:        []string{"\n  replace \"example.com/kit\"  => \"example.com/fork\" v1.4.0 // source\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved, err := Requirements("module example.com/shop\nrequire " + mod + " v1.2.0\n" + tc.savedReplace)
			require.NoError(t, err)
			builderText := "module example.com/shop\n"
			if tc.builderVersion != "" {
				builderText += "require " + mod + " " + tc.builderVersion + "\n"
			}
			builder, err := Requirements(builderText + tc.builderReplace)
			require.NoError(t, err)
			design := map[string]string{
				"design/a.go": "package design\nimport _ \"example.com/kit/a\"\n",
				"design/b.go": "package design\nimport _ \"example.com/kit/b\"\n",
			}

			missing, findings, err := MissingModules(design, saved, builder)

			require.NoError(t, err)
			if tc.missing {
				assert.Equal(t, []module.Version{{Path: mod, Version: "v1.2.0"}}, missing)
			} else {
				assert.Empty(t, missing)
			}
			if len(tc.finding) == 0 {
				assert.Empty(t, findings)
				return
			}
			require.Len(t, findings, 1)
			for _, text := range tc.finding {
				assert.Contains(t, findings[0], text)
			}
		})
	}
}

// TestMissingModulesRejectsInvalidDesign preserves source errors as errors,
// distinct from actionable dependency and replacement findings.
func TestMissingModulesRejectsInvalidDesign(t *testing.T) {
	mod, err := Requirements("module example.com/shop\n")
	require.NoError(t, err)

	missing, findings, err := MissingModules(map[string]string{"design/a.go": "not Go"}, mod, mod)

	assert.ErrorContains(t, err, "design/a.go:1:1")
	assert.Nil(t, missing)
	assert.Nil(t, findings)
}

// TestRequirementsRejectsInvalidModuleText preserves the validation contract
// used when saved revision and build records load their agreed go.mod.
func TestRequirementsRejectsInvalidModuleText(t *testing.T) {
	for _, text := range []string{
		"go 1.27.3\n",
		"module example.com/shop\nreplace example.com/kit =>\n",
	} {
		_, err := Requirements(text)

		assert.Error(t, err)
	}
}
