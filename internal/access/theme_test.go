package access

import "testing"

func TestParseThemeModeAcceptsSupportedPrimerThemes(t *testing.T) {
	for _, value := range []string{
		"system",
		"light",
		"dark",
		"dark_dimmed",
		"light_colorblind",
		"dark_colorblind",
		"light_tritanopia",
		"dark_tritanopia",
	} {
		t.Run(value, func(t *testing.T) {
			if theme, ok := ParseThemeMode(value); !ok || string(theme) != value {
				t.Fatalf("ParseThemeMode(%q) = %q, %v", value, theme, ok)
			}
		})
	}

	if _, ok := ParseThemeMode("dark_high_contrast"); ok {
		t.Fatal("unsupported Primer theme was accepted")
	}
}
