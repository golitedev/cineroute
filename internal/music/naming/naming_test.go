package naming

import "testing"

func TestRender(t *testing.T) {
	base := map[string]string{
		"albumartist": "Outkast", "title": "Aquemini", "year": "1998",
		"format": "FLAC", "bit_depth": "24", "sampling_rate": "96",
	}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range base {
			m[a] = b
		}
		m[k] = v
		return m
	}
	tests := []struct {
		name, tmpl string
		values     map[string]string
		restrict   bool
		want       string
	}{
		{"default", "{albumartist}/{title} ({year})", base, false, "Outkast/Aquemini (1998)"},
		{"slash in value stays in one folder", "{albumartist}/{title}", with("albumartist", "AC/DC"), false, "ACDC/Aquemini"},
		{"empty year drops brackets", "{albumartist}/{title} ({year})", with("year", ""), false, "Outkast/Aquemini"},
		{"quality fields", "{albumartist} - {title} [{format} {bit_depth}-{sampling_rate}]", base, false, "Outkast - Aquemini [FLAC 24-96]"},
		{"invalid characters removed", "{albumartist}/{title}", with("title", `Live: "At" <Home>?*|`), false, "Outkast/Live At Home"},
		{"leading dots cannot hide folders", "{title}", with("title", "...Baby One More Time"), false, "Baby One More Time"},
		{"dot dot is not a path", "{albumartist}/{title}", with("albumartist", ".."), false, "Aquemini"},
		{"restrict to ascii", "{albumartist}/{title}", with("title", "Café Ñoño"), true, "Outkast/Caf oo"},
		{"literal braces", "{{x}} {title}", base, false, "{x} Aquemini"},
		{"empty dash separator trimmed", "{albumartist} - {version}", with("version", ""), false, "Outkast"},
		{"reserved windows name", "{title}", with("title", "CON"), false, "CON_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(tt.tmpl, tt.values, tt.restrict)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderEmptyPath(t *testing.T) {
	if _, err := Render("{version}", map[string]string{}, false); err == nil {
		t.Fatal("expected an error for an empty path")
	}
}

func TestCheck(t *testing.T) {
	for _, bad := range []string{"{nope}", "{title", "title}"} {
		if Check(bad) == nil {
			t.Errorf("Check(%q) = nil, want error", bad)
		}
	}
	if err := Check("{albumartist}/{title} ({year})"); err != nil {
		t.Fatal(err)
	}
	if !Uses("{albumartist} [{bit_depth}]", "bit_depth") || Uses("{title}", "year") {
		t.Fatal("Uses is wrong")
	}
}

func TestTruncateKeepsUTF8(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "é"
	}
	got, err := Render("{title}", map[string]string{"title": long}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxSegmentBytes {
		t.Fatalf("segment is %d bytes", len(got))
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("truncation split a character")
		}
	}
}
