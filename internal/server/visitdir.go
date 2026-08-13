package server

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// latestVisitDir resolves a node's live stage directory under the
// per-visit layout (spec amendment A1: {node}/v{N}, latest mirrored at
// the node root). The v{N} dir with the highest N is the authoritative
// live copy — the root mirror only lands at node completion — so
// serving reads prefer it. A node dir with no v{N} children (old runs)
// resolves to itself.
func latestVisitDir(nodeDir string) string {
	entries, err := os.ReadDir(nodeDir)
	if err != nil {
		return nodeDir
	}
	best, bestN := "", 0
	for _, ent := range entries {
		if !ent.IsDir() || !strings.HasPrefix(ent.Name(), "v") {
			continue
		}
		n, err := strconv.Atoi(ent.Name()[1:])
		if err != nil || n <= bestN {
			continue
		}
		best, bestN = ent.Name(), n
	}
	if best == "" {
		return nodeDir
	}
	return filepath.Join(nodeDir, best)
}

// stageFilePath resolves a stage file for serving: the latest visit
// dir's copy when present, else the node-root (mirror / legacy) copy.
func stageFilePath(nodeDir, name string) string {
	if p := filepath.Join(latestVisitDir(nodeDir), name); fileExistsFS(p) {
		return p
	}
	return filepath.Join(nodeDir, name)
}

func fileExistsFS(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
