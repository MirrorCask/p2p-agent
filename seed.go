package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/images"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type InfoDict struct {
	Name        string `bencode:"name"`
	PieceLength int    `bencode:"piece length"`
	Pieces      []byte `bencode:"pieces"`
	Length      int64  `bencode:"length"`
}

func CalcInfoHashFromFile(filePath string, pieceLength int) (string, error) {
	file, err := os.OpenFile(filePath, os.O_RDONLY, 0)
	if err != nil {
		log.Println("Error opening file:", err)
		return "", err
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil {
		log.Println("Error getting file info:", err)
		return "", err
	}
	var pieceHashes []byte
	buf := make([]byte, pieceLength)
	for {
		n, err := file.Read(buf)
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Println("Error reading file:", err)
			return "", err
		}
		hash := sha1.Sum(buf[:n])
		pieceHashes = append(pieceHashes, hash[:]...)
	}

	info := InfoDict{
		Name:        fileInfo.Name(),
		PieceLength: pieceLength,
		Pieces:      pieceHashes,
		Length:      fileInfo.Size(),
	}

	var bencodeBuffer bytes.Buffer
	encoder := bencode.NewEncoder(&bencodeBuffer)
	if err := encoder.Encode(info); err != nil {
		return "", fmt.Errorf("Bencode encode failed: %w", err)
	}

	infohashBytes := sha1.Sum(bencodeBuffer.Bytes())

	return fmt.Sprintf("%x", infohashBytes), nil
}

func seed(digest, filePath string, client *torrent.Client) error {
	trackerAnnounceURL := os.Getenv("TRACKER_ANNOUNCE_URL")
	if trackerAnnounceURL == "" {
		trackerAnnounceURL = "http://tracker.kube-system.svc.cluster.local:80/announce"
		log.Println("TRACKER_ANNOUNCE_URL is not set, using default:", trackerAnnounceURL)
	}
	serviceInfohash, err := GetInfohash(digest)
	if err != nil {
		log.Println("Error getting infohash:", err)
		return err
	}
	if serviceInfohash == "" {
		log.Println("No infohash found for digest:", digest, "calculateing it now")
		infoHash, err := CalcInfoHashFromFile(filePath, 262144)
		if err != nil {
			log.Println("Error calculating infohash from file:", err)
			return err
		}
		if err := Modify(digest, infoHash); err != nil {
			log.Println("Error modifying infohash:", err)
			return err
		}
		serviceInfohash = infoHash
		log.Println("Successfully modified infohash for digest:", digest, "to", serviceInfohash)
	}
	t, err := client.AddMagnet(fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s&tr=%s", serviceInfohash, digest, trackerAnnounceURL))
	if err != nil {
		log.Println("Error adding magnet link:", err)
		return err
	}
	log.Println("Successfully added magnet link for digest:", digest, "with infohash:", serviceInfohash)
	<-t.GotInfo()
	log.Println("Starting to seed digest:", digest, "with infohash:", serviceInfohash)
	t.DownloadAll()
	return nil
}

func initSeed(ctx context.Context, client *containerd.Client, containerdRoot string, tclient *torrent.Client) error {
	imageStore := client.ImageService()
	imageList, err := imageStore.List(ctx)
	if err != nil {
		return errors.New("Unable to list images")
	}

	if len(imageList) == 0 {
		return errors.New("No images found")
	}

	contentStorePath := filepath.Join(containerdRoot, "io.containerd.content.v1.content")

	blobLocationMap := make(map[digest.Digest]string)

	for _, image := range imageList {
		err := images.Walk(ctx, images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
			d := desc.Digest
			if _, ok := blobLocationMap[d]; ok {
				return nil, nil
			}

			blobPath := filepath.Join(contentStorePath, "blobs", d.Algorithm().String(), d.Encoded())

			if _, err := os.Stat(blobPath); err != nil {
				if os.IsNotExist(err) {
					log.Printf("Warning: Metadata found for digest %s, but file %s does not exist. Skipping...", d, blobPath)
					return nil, nil
				}
			}

			blobLocationMap[d] = blobPath
			return nil, nil
		}), image.Target)

		if err != nil {
			log.Printf("Warning: Error traversing image %s: %v.", image.Name, err)
			continue
		}
	}

	for d, filePath := range blobLocationMap {
		log.Printf("Init seeding digest %s from file %s", d, filePath)
		if err := seed(d.String(), filePath, tclient); err != nil {
			log.Printf("Error init seeding digest %s: %v", d, err)
			log.Println("Skipping...")
			continue
		}
		log.Printf("Successfully init seeded digest %s", d)
	}

	return nil
}
