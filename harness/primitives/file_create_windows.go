package primitives

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateWindowsFilePath(path string) error {
	normalized := strings.ReplaceAll(path, "/", `\`)
	if strings.HasPrefix(normalized, `\\?\`) || strings.HasPrefix(normalized, `\\.\`) ||
		strings.HasPrefix(normalized, `\??\`) {
		return errors.New("device paths are not supported")
	}
	volume := filepath.VolumeName(normalized)
	for _, component := range strings.Split(strings.TrimPrefix(normalized, volume), `\`) {
		if component == "" || component == "." || component == ".." {
			continue
		}
		base, _, _ := strings.Cut(component, ".")
		if strings.Contains(component, ":") || (base != "" && !filepath.IsLocal(strings.TrimRight(base, " "))) ||
			strings.TrimRight(component, ". ") != component {
			return fmt.Errorf("unsupported Windows path component %q", component)
		}
	}
	return nil
}

func createNewPath(ctx context.Context, request IOCreateRequest) (IOCreateResult, error) {
	if request.Kind != IOCreateRegularFile && request.Kind != IOCreateDirectory {
		return IOCreateResult{}, fmt.Errorf("create %q: unsupported kind %d", request.Path, request.Kind)
	}
	if request.Mode&^os.ModePerm != 0 {
		return IOCreateResult{}, errors.New("Windows creation supports permission bits only")
	}
	if err := validateWindowsFilePath(request.Path); err != nil {
		return IOCreateResult{}, err
	}
	path := request.Path
	if request.Kind == IOCreateDirectory {
		path = filepath.Clean(path)
	}
	parentPath, name := filepath.Split(path)
	if parentPath == "" {
		parentPath = "."
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return IOCreateResult{}, fmt.Errorf("open parent directory %q: %w", parentPath, err)
	}
	defer parent.Close()
	for retries := 0; retries < 16; retries++ {
		if err := ctx.Err(); err != nil {
			return IOCreateResult{}, err
		}
		if request.Kind == IOCreateRegularFile {
			file, createErr := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, request.Mode)
			err = createErr
			if err == nil {
				err = errors.Join(file.Sync(), file.Close())
				if err != nil {
					return IOCreateResult{}, err
				}
				return IOCreateResult{Kind: request.Kind}, nil
			}
		} else {
			err = parent.Mkdir(name, request.Mode)
			if err == nil {
				return IOCreateResult{Kind: request.Kind}, nil
			}
		}
		if !errors.Is(err, os.ErrExist) {
			return IOCreateResult{}, fmt.Errorf("create %q: %w", path, err)
		}
		info, err := parent.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return IOCreateResult{}, err
		}
		valid := info.Mode().IsRegular()
		if request.Kind == IOCreateDirectory {
			valid = info.IsDir() && info.Mode()&os.ModeSymlink == 0
		}
		if !valid {
			return IOCreateResult{}, errors.New("existing entry has an incompatible type or is a reparse point")
		}
		return IOCreateResult{Kind: request.Kind}, nil
	}
	return IOCreateResult{}, errors.New("create path remained unstable after 16 attempts")
}
