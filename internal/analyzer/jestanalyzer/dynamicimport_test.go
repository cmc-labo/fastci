package jestanalyzer

import "testing"

func TestScanDynamicImportSites(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		wantOpaque bool
		wantTmpl   int
	}{
		{"static string literal", `import("./foo")`, false, 0},
		{"static string literal, require", `require("./foo")`, false, 0},
		{"opaque variable", `import(name)`, true, 0},
		{"opaque function call", `import(pick())`, true, 0},
		{"opaque concatenation", `import("./x" + suffix)`, true, 0},
		{"plain template, no interpolation", "import(`./foo`)", false, 0},
		{"template with interpolation, solo argument", "import(`./plugins/${name}`)", false, 1},
		{"template with interpolation, part of expression", "import(`./plugins/${name}` + x)", true, 0},
		{"dynamic require", `require(name)`, true, 0},
		{"import( inside a string literal is not a call", `const s = "import(x)";`, false, 0},
		{"import( inside a line comment is not a call", "// import(x)\nconst y = 1;", false, 0},
		{"import( inside a block comment is not a call", "/* import(x) */ const y = 1;", false, 0},
		{
			"nested interpolation containing a string containing a backtick-lookalike",
			"import(`./p/${ `${a}` + \"import(z)\" }`)",
			false, 1,
		},
		{"method literally named import is a false positive, not a false negative", "foo.import(x)", true, 0},
		{"unterminated call is treated as opaque, not a crash", "import(", true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := scanDynamicImportSites([]byte(c.src))
			if s.Opaque != c.wantOpaque {
				t.Errorf("Opaque = %v, want %v", s.Opaque, c.wantOpaque)
			}
			if len(s.TemplateCalls) != c.wantTmpl {
				t.Errorf("len(TemplateCalls) = %d, want %d", len(s.TemplateCalls), c.wantTmpl)
			}
		})
	}
}

func TestScanDynamicImportSitesTemplateCallOffsets(t *testing.T) {
	src := "import(`./plugins/${name}`)"
	s := scanDynamicImportSites([]byte(src))
	if len(s.TemplateCalls) != 1 {
		t.Fatalf("len(TemplateCalls) = %d, want 1", len(s.TemplateCalls))
	}
	call := s.TemplateCalls[0]
	if got := src[call.ArgStart:call.ArgEnd]; got != "`./plugins/${name}`" {
		t.Errorf("call argument text = %q, want the full template literal", got)
	}
	if call.StaticPrefix != "./plugins/" {
		t.Errorf("StaticPrefix = %q, want %q", call.StaticPrefix, "./plugins/")
	}
}

func TestNeutralizeTemplateCalls(t *testing.T) {
	src := []byte("import(`./plugins/${name}`);")
	scan := scanDynamicImportSites(src)
	if len(scan.TemplateCalls) != 1 {
		t.Fatalf("expected 1 template call, got %d", len(scan.TemplateCalls))
	}
	got := neutralizeTemplateCalls(src, scan.TemplateCalls)
	want := `import("__fastci_dynamic__");`
	if got != want {
		t.Errorf("neutralizeTemplateCalls = %q, want %q", got, want)
	}
	// The rewritten source must not itself contain anything that looks like
	// a dynamic call the scanner would flag.
	if rescan := scanDynamicImportSites([]byte(got)); rescan.Opaque || len(rescan.TemplateCalls) != 0 {
		t.Errorf("rewritten source rescans as dynamic: %+v", rescan)
	}
}
