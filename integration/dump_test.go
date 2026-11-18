package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Uktupus/schemadmin"
)

// dumpSchema renders a schema as text, one fact per line, so that a
// mismatch against a golden file reads as a line diff.
func dumpSchema(s *schemadmin.Schema) string {
	var b strings.Builder
	for _, t := range s.Tables {
		kind := "table"
		if t.View {
			kind = "view"
		}
		fmt.Fprintf(&b, "%s %s\n", kind, qualified(t.Schema, t.Name))
		pk := "-"
		if len(t.PrimaryKey) > 0 {
			pk = strings.Join(t.PrimaryKey, ", ")
		}
		fmt.Fprintf(&b, "  pk %s\n", pk)

		for _, c := range t.Columns {
			dbType := c.DBType
			if dbType == "" {
				dbType = "-"
			}
			null := "notnull"
			if c.Nullable {
				null = "null"
			}
			fmt.Fprintf(&b, "  col %s %s %s %s", c.Name, dbType, c.Kind, null)
			if c.AutoIncrement {
				b.WriteString(" auto")
			}
			if c.Generated {
				b.WriteString(" generated")
			}
			if c.HasDefault && !(c.AutoIncrement && c.Default == "") {
				fmt.Fprintf(&b, " default=%s", c.Default)
			}
			if c.MaxLength > 0 {
				fmt.Fprintf(&b, " max=%d", c.MaxLength)
			}
			if len(c.EnumValues) > 0 {
				fmt.Fprintf(&b, " enum=%s", strings.Join(c.EnumValues, "|"))
			}
			b.WriteByte('\n')
		}

		for _, fk := range t.ForeignKeys {
			fmt.Fprintf(&b, "  fk (%s) -> %s(%s) delete=%s update=%s\n",
				strings.Join(fk.Columns, ", "),
				qualified(fk.RefSchema, fk.RefTable),
				strings.Join(fk.RefColumns, ", "),
				fk.OnDelete, fk.OnUpdate)
		}
	}
	return b.String()
}

func qualified(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}

// readTestdata returns the content of a file in testdata.
func readTestdata(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// diffLines reports the lines that differ between want and got.
func diffLines(want, got string) string {
	wantLines := strings.Split(strings.TrimRight(want, "\n"), "\n")
	gotLines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	var b strings.Builder
	for i := 0; i < max(len(wantLines), len(gotLines)); i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w != g {
			fmt.Fprintf(&b, "line %d:\n  want: %s\n  got:  %s\n", i+1, w, g)
		}
	}
	return b.String()
}
