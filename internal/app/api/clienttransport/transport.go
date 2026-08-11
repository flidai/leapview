package clienttransport

import (
	"context"
	"net/http"
	"net/url"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	"github.com/flidai/leapview/internal/platform/cliapi"
)

type Transport struct {
	Target           string
	Token            string
	Client           *http.Client
	MaxResponseBytes int64
	PrepareRequest   func(*http.Request)
}

func (transport Transport) DoAPIGen(
	ctx context.Context,
	request apigenclient.Request,
	out any,
) (apigenclient.Response, error) {
	return (cliapi.HTTPTransport{
		Target:           transport.Target,
		Token:            transport.Token,
		Client:           transport.Client,
		MaxResponseBytes: transport.MaxResponseBytes,
		PrepareRequest:   transport.PrepareRequest,
		AllowsStatus:     apiaggregate.APIGenOperationAllowsStatus,
	}).DoAPIGen(ctx, request, out)
}

func RequestURL(
	target string,
	path string,
	pathParams map[string]string,
	query url.Values,
) (string, error) {
	return cliapi.RequestURL(target, path, pathParams, query)
}
