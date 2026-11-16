// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// objectStore is a bucket, keys relative to the storage's prefix.
type objectStore interface {
	put(ctx context.Context, key string, r io.Reader, size int64) error
	stat(ctx context.Context, key string) (int64, error)
	// list returns the objects directly below dir ("" lists anything).
	list(ctx context.Context, dir string) ([]objectInfo, error)
	remove(ctx context.Context, key string) error
	get(ctx context.Context, key string) (io.ReadCloser, error)
	presign(ctx context.Context, key string, ttl time.Duration) (string, error)
}

type objectInfo struct {
	name string
	size int64
}

type minioStore struct {
	c      *minio.Client
	bucket string
	prefix string
}

func newMinioStore(s helperapi.S3Storage) (objectStore, error) {
	lookup := minio.BucketLookupAuto
	if s.PathStyle {
		lookup = minio.BucketLookupPath
	}
	c, err := minio.New(s.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(s.AccessKey, s.SecretKey, ""), Secure: !s.Insecure, Region: s.Region, BucketLookup: lookup,
	})
	if err != nil {
		return nil, err
	}
	return &minioStore{c: c, bucket: s.Bucket, prefix: strings.Trim(s.Prefix, "/")}, nil
}

func (m *minioStore) key(k string) string {
	if m.prefix == "" {
		return k
	}
	return path.Join(m.prefix, k)
}

func (m *minioStore) put(ctx context.Context, key string, r io.Reader, size int64) error {
	_, err := m.c.PutObject(ctx, m.bucket, m.key(key), r, size, minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

func (m *minioStore) stat(ctx context.Context, key string) (int64, error) {
	info, err := m.c.StatObject(ctx, m.bucket, m.key(key), minio.StatObjectOptions{})
	return info.Size, err
}

func (m *minioStore) list(ctx context.Context, dir string) ([]objectInfo, error) {
	prefix := m.key(dir)
	if prefix != "" {
		prefix += "/"
	}
	var out []objectInfo
	opts := minio.ListObjectsOptions{Prefix: prefix}
	if dir == "" {
		opts.MaxKeys = 1
	}
	for o := range m.c.ListObjects(ctx, m.bucket, opts) {
		if o.Err != nil {
			return nil, o.Err
		}
		name := strings.TrimPrefix(o.Key, prefix)
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		out = append(out, objectInfo{name: name, size: o.Size})
		if dir == "" {
			break
		}
	}
	return out, nil
}

func (m *minioStore) remove(ctx context.Context, key string) error {
	return m.c.RemoveObject(ctx, m.bucket, m.key(key), minio.RemoveObjectOptions{})
}

func (m *minioStore) get(ctx context.Context, key string) (io.ReadCloser, error) {
	o, err := m.c.GetObject(ctx, m.bucket, m.key(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	if _, err := o.Stat(); err != nil {
		_ = o.Close()
		return nil, err
	}
	return o, nil
}

func (m *minioStore) presign(ctx context.Context, key string, ttl time.Duration) (string, error) {
	// The browser saves the file instead of showing it.
	params := url.Values{"response-content-disposition": {`attachment; filename="` + path.Base(key) + `"`}}
	u, err := m.c.PresignedGetObject(ctx, m.bucket, m.key(key), ttl, params)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
