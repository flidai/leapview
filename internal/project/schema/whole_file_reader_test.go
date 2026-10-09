package configschema

import (
	"fmt"
	"testing"
)

func TestOptionlessSourceReadersRejectUnsupportedOptions(t *testing.T) {
	for _, format := range []string{"text", "blob", "vortex"} {
		t.Run(format, func(t *testing.T) {
			document := func(options string) []byte {
				return []byte(fmt.Sprintf("apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:content, name: content}\nspec:\n  connection: local\n  location: {type: path, path: content.%s, format: %s%s}\n", format, format, options))
			}
			if err := ValidateBytes(KindSource, format+".yaml", document("")); err != nil {
				t.Fatalf("optionless whole-file reader: %v", err)
			}
			for _, options := range []string{", options: {}", ", options: {header: false}", ", options: {compression: auto}", ", options: {version: '1'}"} {
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

func TestTableReaderIDsRequireExactDecimalStrings(t *testing.T) {
	for _, tc := range []struct{ format, option string }{{"delta", "version"}, {"iceberg", "snapshot"}} {
		t.Run(tc.format, func(t *testing.T) {
			document := func(value string) []byte {
				return []byte(fmt.Sprintf("apiVersion: leapview.dev/v1\nkind: Source\nmetadata: {id: source:content, name: content}\nspec:\n  connection: local\n  location: {type: path, path: content, format: %s, options: {%s: %s}}\n", tc.format, tc.option, value))
			}
			for _, value := range []string{"'0'", "'5298355539581857556'", "'9223372036854775807'"} {
				if err := ValidateBytes(KindSource, "valid.yaml", document(value)); err != nil {
					t.Fatalf("exact ID %s rejected: %v", value, err)
				}
			}
			for _, value := range []string{"0", "5298355539581857556", "'-1'", "'+1'", "'01'", "'1.5'", "'1e3'", "'not-an-id'", "''", "'18446744073709551615'", "'184467440737095516150'"} {
				if err := ValidateBytes(KindSource, "invalid.yaml", document(value)); err == nil {
					t.Fatalf("malformed or numeric ID %s accepted", value)
				}
			}
		})
	}
}
