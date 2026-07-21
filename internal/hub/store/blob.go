package store

import (
	"context"
	"crypto/rand"
	"fmt"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
	"gocloud.dev/secrets"
	"gocloud.dev/secrets/localsecrets"
)

// BlobStore is the object-storage seam for large opaque bodies. It is a thin
// wrapper over gocloud.dev/blob so the backend (memblob for tests, fileblob for
// local dev, a cloud bucket in prod) is a URL/config swap, not a code change.
type BlobStore interface {
	Put(ctx context.Context, key string, data []byte) error
	// Get returns ErrNotFound when the key is absent.
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Close() error
}

// SecretResolver is the secret seam backed by gocloud.dev/secrets. It resolves
// named secrets (host-token pepper, cookie key, DB creds — P0-3/P0-4). No bytes
// need flow this slice; the resolver seam is the deliverable. Encrypt/Decrypt
// round-trip through the configured Keeper (localsecrets in dev/test, a cloud
// KMS in prod).
type SecretResolver interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
	// Close releases the underlying keeper. A no-op for localsecrets, but a
	// cloud KMS keeper holds a client/connection that must be released.
	Close() error
}

// gocloudBlob adapts a *blob.Bucket to BlobStore.
type gocloudBlob struct {
	bucket *blob.Bucket
}

func (g *gocloudBlob) Put(ctx context.Context, key string, data []byte) error {
	return g.bucket.WriteAll(ctx, key, data, nil)
}

func (g *gocloudBlob) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := g.bucket.ReadAll(ctx, key)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

func (g *gocloudBlob) Delete(ctx context.Context, key string) error {
	return g.bucket.Delete(ctx, key)
}

func (g *gocloudBlob) Close() error {
	return g.bucket.Close()
}

// gocloudSecrets adapts a *secrets.Keeper to SecretResolver.
type gocloudSecrets struct {
	keeper *secrets.Keeper
}

func (g *gocloudSecrets) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	return g.keeper.Encrypt(ctx, plaintext)
}

func (g *gocloudSecrets) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	return g.keeper.Decrypt(ctx, ciphertext)
}

func (g *gocloudSecrets) Close() error {
	return g.keeper.Close()
}

// newRandomKeeper builds a localsecrets keeper with a per-process random key.
// This is enough to exercise the resolver seam in dev/test; a prod deployment
// swaps in a cloud KMS keeper via a URL (see PGConfig.SecretURL). The key lives
// only in memory for the process lifetime, so ciphertext does not survive a
// restart — acceptable because no durable secret bytes flow this slice.
func newRandomKeeper() (SecretResolver, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, fmt.Errorf("store: generate secret key: %w", err)
	}
	return &gocloudSecrets{keeper: localsecrets.NewKeeper(key)}, nil
}
