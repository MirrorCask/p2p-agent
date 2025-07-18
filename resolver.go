package main

import (
	"context"
	"log"

	"github.com/anacrolix/torrent"
	"github.com/containerd/containerd/remotes"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type P2PResolver struct {
	fallback  remotes.Resolver
	p2pclient *torrent.Client
}

func NewP2PResolver(fallback remotes.Resolver, p2pclient *torrent.Client) *P2PResolver {
	return &P2PResolver{
		fallback:  fallback,
		p2pclient: p2pclient,
	}
}

func (r *P2PResolver) Resolve(ctx context.Context, ref string) (string, ocispec.Descriptor, error) {
	name, desc, err := r.fallback.Resolve(ctx, ref)
	if err != nil {
		log.Println("Failed to resolve reference:", ref, "Error:", err)
		return "", ocispec.Descriptor{}, err
	}
	return name, desc, nil
}

func (r *P2PResolver) Fetcher(ctx context.Context, ref string) (remotes.Fetcher, error) {
	fallbackFetcher, err := r.fallback.Fetcher(ctx, ref)
	if err != nil {
		log.Println("Failed to create fallback fetcher for reference:", ref, "Error:", err)
		return nil, err
	}
	return NewP2PFetcher(fallbackFetcher, r.p2pclient), nil
}

func (r *P2PResolver) Pusher(ctx context.Context, ref string) (remotes.Pusher, error) {
	return r.fallback.Pusher(ctx, ref)
}
