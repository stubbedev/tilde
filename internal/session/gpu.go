package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func (d UdevDevice) BootVGA() (bool, error) {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(d.Syspath)), "boot_vga"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("session: boot_vga of %s: %w", d.Devnode, err)
	}
	return strings.TrimSpace(string(b)) == "1", nil
}

func (d UdevDevice) HasRenderNode() (bool, error) {
	nodes, err := filepath.Glob(filepath.Join(filepath.Dir(d.Syspath), "renderD*"))
	if err != nil {
		return false, fmt.Errorf("session: render nodes beside %s: %w", d.Devnode, err)
	}
	return len(nodes) > 0, nil
}
