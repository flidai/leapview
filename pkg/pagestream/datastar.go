package pagestream

import (
	"net/http"

	ds "github.com/starfederation/datastar-go/datastar"
)

func ReadSignals(r *http.Request, target any) error {
	return ds.ReadSignals(r, target)
}
