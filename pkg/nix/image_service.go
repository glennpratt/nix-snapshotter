package nix

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/log"
	"github.com/pdtpartners/nix-snapshotter/pkg/nix2container"
	"google.golang.org/grpc"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"
)

var (
	ErrNotInitialized = errors.New("Nix-snapshotter Image Service not yet initialized")
)

// ImageServiceConfig is used to configure the image service instance.
type ImageServiceConfig struct {
	Config
}

// ImageServiceOpt is an option for NewImageService.
type ImageServiceOpt interface {
	SetImageServiceOpt(cfg *ImageServiceConfig)
}

type imageService struct {
	runtime.UnimplementedImageServiceServer
	mu                 sync.Mutex
	client             *client.Client
	imageServiceClient runtime.ImageServiceClient
	nixBuilder         NixBuilder
}

func NewImageService(ctx context.Context, containerdAddr string, opts ...ImageServiceOpt) (runtime.ImageServiceServer, error) {
	cfg := ImageServiceConfig{
		Config: Config{
			nixBuilder: defaultNixBuilder,
		},
	}
	for _, opt := range opts {
		opt.SetImageServiceOpt(&cfg)
	}

	service := &imageService{
		nixBuilder: cfg.nixBuilder,
	}

	go func() {
		log.G(ctx).Debugf("Waiting for CRI service is started...")
		for i := 0; i < 100; i++ {
			client, err := client.New(containerdAddr)
			if err == nil {
				service.mu.Lock()
				service.client = client
				service.imageServiceClient = runtime.NewImageServiceClient(client.Conn().(*grpc.ClientConn))
				service.mu.Unlock()
				log.G(ctx).Info("Connected to backend CRI service")
				return
			}
			log.G(ctx).WithError(err).Warnf("Failed to connect to CRI")
			time.Sleep(10 * time.Second)
		}
		log.G(ctx).Warnf("No connection is available to CRI")
	}()

	return service, nil
}

func (is *imageService) getClient() runtime.ImageServiceClient {
	is.mu.Lock()
	client := is.imageServiceClient
	is.mu.Unlock()
	return client
}

// ListImages lists existing images.
func (is *imageService) ListImages(ctx context.Context, req *runtime.ListImagesRequest) (*runtime.ListImagesResponse, error) {
	client := is.getClient()
	if client == nil {
		return nil, ErrNotInitialized
	}
	return client.ListImages(ctx, req)
}

// ImageStatus returns the status of the image. If the image is not
// present, returns a response with ImageStatusResponse.Image set to
// nil.
func (is *imageService) ImageStatus(ctx context.Context, req *runtime.ImageStatusRequest) (*runtime.ImageStatusResponse, error) {
	client := is.getClient()
	if client == nil {
		return nil, ErrNotInitialized
	}
	return client.ImageStatus(ctx, req)
}

// PullImage pulls an image with authentication config.
func (is *imageService) PullImage(ctx context.Context, req *runtime.PullImageRequest) (*runtime.PullImageResponse, error) {
	client := is.getClient()
	if client == nil {
		return nil, ErrNotInitialized
	}

	ref := req.Image.Image
	if !strings.HasPrefix(ref, nix2container.ImageRefPrefix) {
		log.G(ctx).WithField("ref", ref).Info("[image-service] Falling back to CRI pull image")
		resp, err := client.PullImage(ctx, req)
		return resp, err
	}
	archivePath, digest := parseNixRef(ref)

	_, err := os.Stat(archivePath)
	if errors.Is(err, os.ErrNotExist) {
		log.G(ctx).Info("[image-service] Pulling nix image archive")
		err := is.nixBuilder(ctx, "", archivePath)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	log.G(ctx).Info("[image-service] Loading nix image archive")
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	img, err := nix2container.Load(ctx, is.client, archivePath)
	if err != nil {
		return nil, err
	}

	configDesc, err := img.Config(ctx)
	if err != nil {
		return nil, err
	}
	imageRef := configDesc.Digest.String()
	if digest != "" && digest != imageRef && digest != img.Target().Digest.String() {
		return nil, fmt.Errorf("nix image %s has digest %s (manifest %s), not %s",
			archivePath, imageRef, img.Target().Digest, digest)
	}

	log.G(ctx).WithField("imageRef", imageRef).Info("[image-service] Successfully pulled image")
	return &runtime.PullImageResponse{
		ImageRef: imageRef,
	}, nil
}

// parseNixRef splits a "nix:0/nix/store/...tar" ref into the archive's path
// and the digest after an "@", if any. A tag (":latest", which the kubelet
// adds to a bare ref, or any other a chart appends) is ignored: the store
// path is the image's identity, as a digest is when a ref has both. A store
// path's name can't hold ':' or '@', so neither is ambiguous.
func parseNixRef(ref string) (archivePath, digest string) {
	archivePath = strings.TrimPrefix(ref, nix2container.ImageRefPrefix)
	if i := strings.Index(archivePath, "@"); i >= 0 {
		archivePath, digest = archivePath[:i], archivePath[i+1:]
	}
	if i := strings.LastIndex(archivePath, ":"); i > strings.LastIndex(archivePath, "/") {
		archivePath = archivePath[:i]
	}
	return archivePath, digest
}

// RemoveImage removes the image.
// This call is idempotent, and must not return an error if the image has
// already been removed.
func (is *imageService) RemoveImage(ctx context.Context, req *runtime.RemoveImageRequest) (*runtime.RemoveImageResponse, error) {
	client := is.getClient()
	if client == nil {
		return nil, ErrNotInitialized
	}
	return client.RemoveImage(ctx, req)
}

// ImageFSInfo returns information of the filesystem that is used to store images.
func (is *imageService) ImageFsInfo(ctx context.Context, req *runtime.ImageFsInfoRequest) (*runtime.ImageFsInfoResponse, error) {
	client := is.getClient()
	if client == nil {
		return nil, ErrNotInitialized
	}
	return client.ImageFsInfo(ctx, req)
}
