package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/containerd/containerd/remotes"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type P2PFetcher struct {
	fallback  remotes.Fetcher
	p2pclient *torrent.Client
}

func NewP2PFetcher(fallback remotes.Fetcher, p2pclient *torrent.Client) *P2PFetcher {
	return &P2PFetcher{
		fallback:  fallback,
		p2pclient: p2pclient,
	}
}

func (f *P2PFetcher) Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	reader, err := P2PFetch(ctx, desc, f.p2pclient)
	if err == nil {
		log.Println("Successfully fetched data using P2P")
		return reader, nil
	}

	log.Println("P2P fetch failed, falling back to original fetcher")
	fallbackReader, err := FallbackFetch(ctx, desc, f.fallback)
	if err != nil {
		log.Println("Fallback fetch failed:", err)
		return nil, err
	}
	return fallbackReader, nil
}

func WaitP2PDownloadComplete(ctx context.Context, t *torrent.Torrent) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			bytesCompleted := t.BytesCompleted()
			totalLength := t.Length()
			if bytesCompleted >= totalLength {
				log.Printf("Torrent %s download complete!", t.Name())
				return nil
			}
		}
	}
}

func P2PFetch(ctx context.Context, desc ocispec.Descriptor, p2pclient *torrent.Client) (io.ReadCloser, error) {
	infohash, err := GetInfohash(desc.Digest.String())
	if err != nil {
		log.Println("Error while infohash:", err)
	}
	if infohash == "" {
		return nil, fmt.Errorf("infohash not found for digest: %s", desc.Digest.String())
	}
	trackerAnnounceURL := os.Getenv("TRACKER_ANNOUNCE_URL")
	if trackerAnnounceURL == "" {
		trackerAnnounceURL = "http://tracker.kube-system.svc.cluster.local:80/announce"
		log.Println("TRACKER_ANNOUNCE_URL is not set, using default:", trackerAnnounceURL)
	}
	t, err := p2pclient.AddMagnet(fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s&tr=%s", infohash, desc.Digest.String(), trackerAnnounceURL))
	if err != nil {
		log.Println("Error adding magnet link:", err)
		return nil, err
	}
	log.Println("Successfully added magnet link for digest:", desc.Digest.String(), "with infohash:", infohash)
	<-t.GotInfo()
	t.DownloadAll()
	WaitP2PDownloadComplete(ctx, t)
	blobPath := t.Files()[0].Path()
	return os.Open(blobPath)
}

func FallbackFetch(ctx context.Context, desc ocispec.Descriptor, fallback remotes.Fetcher) (io.ReadCloser, error) {
	return fallback.Fetch(ctx, desc)
}
