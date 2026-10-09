package configschema

import (
	"fmt"
	"testing"
)

func TestWholeFileReadersRejectUnsupportedOptions(t *testing.T) {
	for _, format := range []string{"text", "blob"} {
		t.Run(format, func(t *testing.T) {
			document := func(options string) []byte {
				return []byte(fmt.Sprintf("apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:content, name: content}\nspec:\n  connection: local\n  location: {type: path, path: content.%s, format: %s%s}\n", format, format, options))
			}
			if err := ValidateBytes(KindSource, format+".yaml", document("")); err != nil {
				t.Fatalf("optionless whole-file reader: %v", err)
			}
			for _, options := range []string{", options: {}", ", options: {header: false}", ", options: {compression: auto}"} {
				if err := ValidateBytes(KindSource, format+".yaml", document(options)); err == nil {
					t.Fatalf("whole-file %s reader accepted unsupported %s", format, options)
				}
			}
			control := "apiVersion: leapview.dev/v1\nkind: Connection\nmetadata: {id: connection:local, name: local}\nspec:\n  type: managed\n"
			if err := ValidateBytes(KindConnection, "local.yaml", []byte(control)); err != nil {
				t.Fatalf("managed connection control: %v", err)
			}
			connection := control + fmt.Sprintf("  readerDefaults: {%s: {}}\n", format)
			if err := ValidateBytes(KindConnection, format+"-defaults.yaml", []byte(connection)); err == nil {
				t.Fatalf("connection accepted unsupported %s reader defaults", format)
			}
		})
	}
}
