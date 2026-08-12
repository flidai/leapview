package module

import "github.com/flidai/leapview/internal/access/avatar"

// Avatar blob aliases keep application composition on the access module
// surface while the image implementation remains owned by access.
type AvatarBlob = avatar.Blob
type AvatarBlobStore = avatar.BlobStore

var ErrAvatarBlobNotFound = avatar.ErrBlobNotFound

func AvatarURL(principalID string, value avatar.Metadata) string {
	return avatar.URLForPrincipal(principalID, value)
}
