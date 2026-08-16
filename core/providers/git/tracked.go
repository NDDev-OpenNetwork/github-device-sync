package git

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type TrackedPath struct {
	Mode string `json:"mode"`
	OID  string `json:"oid"`
	Path string `json:"path"`
}

func (runner *Runner) TrackedPaths(ctx context.Context, directory string) ([]TrackedPath, error) {
	result, err := runner.Run(ctx, directory, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	entries := strings.Split(string(result.Stdout), "\x00")
	paths := make([]TrackedPath, 0, len(entries))
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		metadata, path, found := strings.Cut(entry, "\t")
		if !found {
			return nil, fmt.Errorf("malformed git ls-files record")
		}
		fields := strings.Fields(metadata)
		if len(fields) != 3 || fields[2] != "0" {
			continue
		}
		paths = append(paths, TrackedPath{Mode: fields[0], OID: fields[1], Path: path})
	}
	sort.Slice(paths, func(left, right int) bool { return paths[left].Path < paths[right].Path })
	return paths, nil
}
