package module

import (
	"database/sql"
	"net/http"

	"github.com/flidai/leapview/internal/admin/product"
)

// Product aliases keep process composition on the admin module surface while
// the product implementation remains private to the administration capability.
type ProductService = product.Service
type ProductStatus = product.Status
type ProductBlob = product.Blob
type ProductBlobStore = product.BlobStore
type ProductCommandExecutor = product.CommandExecutor
type ProductCommandFailureWriter = product.CommandFailureWriter
type ProductAuthenticationStatus = product.AuthenticationStatus
type ProductAvailability = product.Availability
type ProductNamedAvailability = product.NamedAvailability
type ProductAPIStatus = product.APIStatus
type ProductSystemStatus = product.SystemStatus
type ProductAgentStatus = product.AgentStatus
type ProductLimits = product.Limits

var ErrProductLogoNotFound = product.ErrNotFound

func NewProductService(database *sql.DB, blobs ProductBlobStore) (*ProductService, error) {
	return product.New(database, blobs)
}

func (m *Module) GetProductSettings(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.product == nil {
		http.NotFound(w, r)
		return
	}
	m.product.GetSettings(w, r)
}

func (m *Module) UpdateProductSettings(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.product == nil {
		http.NotFound(w, r)
		return
	}
	m.product.PatchSettings(w, r)
}

func (m *Module) UploadProductLogo(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.product == nil {
		http.NotFound(w, r)
		return
	}
	m.product.UploadLogo(w, r)
}

func (m *Module) DeleteProductLogo(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.product == nil {
		http.NotFound(w, r)
		return
	}
	m.product.DeleteLogo(w, r)
}

func (m *Module) GetProductLogo(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.product == nil {
		http.NotFound(w, r)
		return
	}
	m.product.GetLogo(w, r)
}

func (m *Module) GetProductAuthenticationStatus(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.product == nil {
		http.NotFound(w, r)
		return
	}
	m.product.GetAuthentication(w, r)
}

func (m *Module) GetProductSystemStatus(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.product == nil {
		http.NotFound(w, r)
		return
	}
	m.product.GetSystem(w, r)
}

func (m *Module) GetProductAPIStatus(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.product == nil {
		http.NotFound(w, r)
		return
	}
	m.product.GetAPIStatus(w, r)
}
