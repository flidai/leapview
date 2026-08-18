package catalogseal

import (
	"context"
	"fmt"
	"io"
	"strings"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client is deliberately tiny so MinIO and AWS clients can be supplied by
// composition without leaking credentials into catalogseal.
type S3Client interface {
	PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	HeadObject(context.Context, *awss3.HeadObjectInput, ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
}

type S3ObjectStore struct {
	Client S3Client
	Bucket string
	Prefix string
}

func NewS3ObjectStore(client S3Client, bucket, prefix string) (*S3ObjectStore, error) {
	if client == nil || strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("S3 catalog client and bucket are required")
	}
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if strings.ContainsAny(prefix, "\\\x00\r\n") || strings.Contains(prefix, "..") {
		return nil, fmt.Errorf("S3 catalog prefix is invalid")
	}
	return &S3ObjectStore{Client: client, Bucket: bucket, Prefix: prefix}, nil
}

func (s *S3ObjectStore) key(key string) (string, error) {
	key = strings.Trim(strings.ReplaceAll(strings.TrimSpace(key), "\\", "/"), "/")
	if key == "" || strings.Contains(key, "..") || strings.Contains(key, "//") {
		return "", ErrObjectUpload
	}
	if s.Prefix == "" {
		return key, nil
	}
	return s.Prefix + "/" + key, nil
}

func (s *S3ObjectStore) Create(ctx context.Context, key string, body io.Reader, metadata ObjectMetadata) error {
	full, err := s.key(key)
	if err != nil || body == nil || metadata == nil {
		return ErrObjectUpload
	}
	_, err = s.Client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: &s.Bucket, Key: &full, Body: body, Metadata: map[string]string(metadata), IfNoneMatch: strptr("*"),
	})
	if err == nil {
		return nil
	}
	// Provider error classes vary (including MinIO). Seal reconciles any
	// acknowledgement failure by opening the exact key and verifying bytes.
	return ErrObjectAmbiguous
}

func (s *S3ObjectStore) Open(ctx context.Context, key string) (Object, error) {
	full, err := s.key(key)
	if err != nil {
		return Object{}, err
	}
	head, err := s.Client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &s.Bucket, Key: &full})
	if err != nil {
		return Object{}, err
	}
	out, err := s.Client.GetObject(ctx, &awss3.GetObjectInput{Bucket: &s.Bucket, Key: &full})
	if err != nil {
		return Object{}, err
	}
	if out.Body == nil {
		return Object{}, ErrObjectCorrupt
	}
	var size int64
	if head.ContentLength != nil {
		size = *head.ContentLength
	}
	metadata := ObjectMetadata{}
	for k, v := range head.Metadata {
		metadata[k] = v
	}
	return Object{Body: out.Body, Size: size, Metadata: metadata}, nil
}

func strptr(value string) *string { return &value }
