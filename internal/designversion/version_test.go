// This file tests the gofmt form of a design file's version and its digest.
package designversion

import (
	"go/scanner"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCanonicalIsTheGofmtFormAndKeepsIt formats texts that differ from gofmt
// in layout, import order and line endings, and one already in gofmt form,
// and checks that each comes out as gofmt prints it, that formatting the
// result again changes nothing, and that its digest names that form.
func TestCanonicalIsTheGofmtFormAndKeepsIt(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "layout",
			src:  "package design\nvar   A=1\nfunc f( ) int {\nreturn  A}\n",
			want: "package design\n\nvar A = 1\n\nfunc f() int {\n\treturn A\n}\n",
		},
		{
			name: "import order",
			src:  "package design\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n)\n\nvar _, _ = fmt.Sprint, strings.Cut\n",
			want: "package design\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nvar _, _ = fmt.Sprint, strings.Cut\n",
		},
		{
			name: "CRLF line ends",
			src:  "package design\r\n\r\nvar A = 1\r\n",
			want: "package design\n\nvar A = 1\n",
		},
		{
			name: "already in gofmt form",
			src:  "package design\n\nvar A = 1\n",
			want: "package design\n\nvar A = 1\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonical("design/types.go", tc.src)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			again, err := Canonical("design/types.go", got)
			require.NoError(t, err)
			assert.Equal(t, got, again, "the gofmt form is kept")
			assert.Equal(t, Digest(tc.want), Digest(got), "one gofmt form, one version")
		})
	}
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Digest(""),
		"a version is named by the SHA-256 of its text")
}

// TestCanonicalNamesTheFileAndLineItCannotParse formats texts Go cannot parse
// as a whole source file and checks that the error names the file and line.
func TestCanonicalNamesTheFileAndLineItCannotParse(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "conflict markers",
			src:  "package design\n\n<<<<<<< draft\nvar A = 10\n=======\nvar A = 11\n>>>>>>> project folder\n",
			want: "design/types.go:3:1: expected declaration, found '<<'",
		},
		{
			name: "no package clause",
			src:  "var A = 1\n",
			want: "design/types.go:1:1: expected 'package', found 'var'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonical("design/types.go", tc.src)
			assert.ErrorContains(t, err, tc.want)
			var list scanner.ErrorList
			assert.ErrorAs(t, err, &list)
			assert.Empty(t, got)
		})
	}
}
