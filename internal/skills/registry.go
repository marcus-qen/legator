/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package skills

import (
	"context"
	"encoding/json"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

// RegistryClient pushes and pulls skill artifacts from OCI registries.
type RegistryClient struct {
	// PlainHTTP allows insecure registries (for dev/test).
	PlainHTTP bool
	// Username for registry auth (optional — uses anon if empty).
	Username string
	// Password for registry auth.
	Password string
}

// NewRegistryClient creates a client for OCI registry operations.
func NewRegistryClient() *RegistryClient {
	return &RegistryClient{}
}

// WithAuth sets credentials for registry authentication.
func (rc *RegistryClient) WithAuth(username, password string) *RegistryClient {
	rc.Username = username
	rc.Password = password
	return rc
}

// WithPlainHTTP enables HTTP (non-TLS) for dev registries.
func (rc *RegistryClient) WithPlainHTTP(plain bool) *RegistryClient {
	rc.PlainHTTP = plain
	return rc
}

// Push packages a skill directory and pushes it to an OCI registry.
func (rc *RegistryClient) Push(ctx context.Context, dir string, ref *OCIRef) (*PushResult, error) {
	// Pack the skill
	packed, err := Pack(dir)
	if err != nil {
		return nil, fmt.Errorf("pack skill: %w", err)
	}

	// Build OCI manifest in memory store
	store := memory.New()

	// Push config blob
	configDesc, err := oras.PushBytes(ctx, store, MediaTypeConfig, packed.Config)
	if err != nil {
		return nil, fmt.Errorf("push config to memory: %w", err)
	}

	// Push content layer
	contentDesc, err := oras.PushBytes(ctx, store, MediaTypeContent, packed.Content)
	if err != nil {
		return nil, fmt.Errorf("push content to memory: %w", err)
	}

	// Pack manifest
	artifactType := "application/vnd.legator.skill.v1"
	packOpts := oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{contentDesc},
	}
	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, artifactType, packOpts)
	if err != nil {
		return nil, fmt.Errorf("pack manifest: %w", err)
	}

	// Tag it
	tag := ref.Tag
	if tag == "" {
		tag = "latest"
	}
	if err := store.Tag(ctx, manifestDesc, tag); err != nil {
		return nil, fmt.Errorf("tag manifest: %w", err)
	}

	// Connect to remote registry
	repo, err := rc.repository(ref)
	if err != nil {
		return nil, fmt.Errorf("connect registry: %w", err)
	}

	// Copy from memory store to remote
	copyDesc, err := oras.Copy(ctx, store, tag, repo, tag, oras.DefaultCopyOptions)
	if err != nil {
		return nil, fmt.Errorf("push to registry: %w", err)
	}

	return &PushResult{
		Ref:        ref.String(),
		Digest:     copyDesc.Digest.String(),
		ConfigSize: configDesc.Size,
		ContentSize: contentDesc.Size,
		Files:      packed.Manifest.Files,
	}, nil
}

// Pull downloads a skill from an OCI registry and returns the content bytes.
func (rc *RegistryClient) Pull(ctx context.Context, ref *OCIRef) ([]byte, *PullResult, error) {
	// Connect to remote
	repo, err := rc.repository(ref)
	if err != nil {
		return nil, nil, fmt.Errorf("connect registry: %w", err)
	}

	// Pull to memory store
	store := memory.New()
	tag := ref.Tag
	if tag == "" && ref.Digest == "" {
		tag = "latest"
	}

	pullRef := tag
	if ref.Digest != "" {
		pullRef = ref.Digest
	}

	manifestDesc, err := oras.Copy(ctx, repo, pullRef, store, pullRef, oras.DefaultCopyOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("pull from registry: %w", err)
	}

	// Read manifest to find content layer
	manifestBytes, err := store.Fetch(ctx, manifestDesc)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch manifest: %w", err)
	}

	manifestData, err := readAll(manifestBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read manifest: %w", err)
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, nil, fmt.Errorf("parse manifest: %w", err)
	}

	// Find content layer
	var contentData []byte
	var configData []byte
	for _, layer := range manifest.Layers {
		if layer.MediaType == MediaTypeContent {
			reader, err := store.Fetch(ctx, layer)
			if err != nil {
				return nil, nil, fmt.Errorf("fetch content layer: %w", err)
			}
			contentData, err = readAll(reader)
			if err != nil {
				return nil, nil, fmt.Errorf("read content layer: %w", err)
			}
		}
	}

	// Read config for metadata
	if manifest.Config.Size > 0 {
		reader, err := store.Fetch(ctx, manifest.Config)
		if err == nil {
			configData, _ = readAll(reader)
		}
	}

	if contentData == nil {
		return nil, nil, fmt.Errorf("no content layer found in manifest")
	}

	result := &PullResult{
		Ref:    ref.String(),
		Digest: manifestDesc.Digest.String(),
		Size:   manifestDesc.Size,
	}
	if configData != nil {
		var sm SkillManifest
		if json.Unmarshal(configData, &sm) == nil {
			result.Name = sm.Name
			result.Files = sm.Files
		}
	}

	return contentData, result, nil
}

// PullToDir downloads and extracts a skill to a directory.
func (rc *RegistryClient) PullToDir(ctx context.Context, ref *OCIRef, destDir string) (*PullResult, error) {
	content, result, err := rc.Pull(ctx, ref)
	if err != nil {
		return nil, err
	}

	if err := Unpack(content, destDir); err != nil {
		return nil, fmt.Errorf("unpack: %w", err)
	}

	return result, nil
}

// repository creates a remote.Repository for the given reference.
func (rc *RegistryClient) repository(ref *OCIRef) (*remote.Repository, error) {
	repoRef := fmt.Sprintf("%s/%s", ref.Registry, ref.Path)
	repo, err := remote.NewRepository(repoRef)
	if err != nil {
		return nil, err
	}

	repo.PlainHTTP = rc.PlainHTTP

	if rc.Username != "" {
		repo.Client = &auth.Client{
			Client: retry.DefaultClient,
			Credential: auth.StaticCredential(ref.Registry, auth.Credential{
				Username: rc.Username,
				Password: rc.Password,
			}),
		}
	}

	return repo, nil
}

// PushResult holds the result of pushing a skill to a registry.
type PushResult struct {
	Ref         string   `json:"ref"`
	Digest      string   `json:"digest"`
	ConfigSize  int64    `json:"configSize"`
	ContentSize int64    `json:"contentSize"`
	Files       []string `json:"files"`
}

// PullResult holds the result of pulling a skill from a registry.
type PullResult struct {
	Ref    string   `json:"ref"`
	Digest string   `json:"digest"`
	Size   int64    `json:"size"`
	Name   string   `json:"name,omitempty"`
	Files  []string `json:"files,omitempty"`
}

// readAll reads all bytes from an io.ReadCloser and closes it.
func readAll(rc interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 32*1024)
	for {
		n, err := rc.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return buf, err
		}
	}
	return buf, nil
}
