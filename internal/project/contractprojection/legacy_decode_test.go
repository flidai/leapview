package contractprojection

import "testing"

func TestHistoricalV1PublicationsKeepExactDigestReplay(t *testing.T) {
	source := []byte(`{"apiVersion":"leapview.dev/v1","contract":{"schema":{"fields":{"id":{"datatype":"String","nullable":false}},"mode":"compatible"}},"kind":"Source","metadata":{"contract":{"compatibility":"backward","version":"1.0.0"},"id":"source:orders","name":"orders"},"profile":"leapview.contract/v1"}`)
	view, err := DecodeSourcePublication(source)
	if err != nil {
		t.Fatal(err)
	}
	if view.Contract.Fields["id"].Datatype == nil || *view.Contract.Fields["id"].Datatype != "String" {
		t.Fatalf("historical field lost: %#v", view.Contract.Fields)
	}
	if digest, err := DigestSourcePublication(source); err != nil || digest != digestCanonicalBytes(source) {
		t.Fatalf("historical source digest = %q, %v", digest, err)
	}

	model := []byte(`{"apiVersion":"leapview.dev/v1","contract":{"definition":{"source":"source:orders","type":"direct"},"entities":{},"fields":{"id":{"datatype":"String","nullable":false}},"grain":{"entity":"order"}},"kind":"Model","metadata":{"contract":{"compatibility":"backward","version":"1.0.0"},"id":"model:orders","name":"orders"},"profile":"leapview.contract/v1"}`)
	modelView, err := DecodeModelPublication(model)
	if err != nil {
		t.Fatal(err)
	}
	if modelView.Contract.Fields["id"].Datatype == nil || *modelView.Contract.Fields["id"].Datatype != "String" {
		t.Fatalf("historical model field lost: %#v", modelView.Contract.Fields)
	}
	if digest, err := DigestModelPublication(model); err != nil || digest != digestCanonicalBytes(model) {
		t.Fatalf("historical model digest = %q, %v", digest, err)
	}
}
