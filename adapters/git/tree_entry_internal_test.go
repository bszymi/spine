package git

import "testing"

func TestParseTreeEntryType(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
		wantErr            bool
	}{
		{name: "empty listing", output: "", want: ""},
		{name: "exact blob", output: "100644 blob abc\tdocs/x.md\x00", want: "blob"},
		{name: "gitlink", output: "160000 commit abc\tdocs/x.md\x00", want: "commit"},
		{name: "other path only", output: "100644 blob abc\tdocs/y.md\x00", want: ""},
		{name: "picks the exact record", output: "040000 tree abc\tdocs\x00100644 blob def\tdocs/x.md\x00", want: "blob"},
		{name: "path with tab and newline", output: "100644 blob abc\tdocs/a\tb\nc.md\x00", want: ""},
		{name: "no tab", output: "100644 blob abc docs/x.md\x00", wantErr: true},
		{name: "short meta", output: "100644 abc\tdocs/x.md\x00", wantErr: true},
		{name: "unknown type", output: "100644 tag abc\tdocs/x.md\x00", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTreeEntryType(tc.output, "docs/x.md")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	// A path containing a tab is matched whole: Cut splits at the first
	// tab only, which is the separator ls-tree writes.
	if got, err := parseTreeEntryType("100644 blob abc\tdocs/a\tb.md\x00", "docs/a\tb.md"); err != nil || got != "blob" {
		t.Fatalf("tab in path: got %q, %v", got, err)
	}
}
