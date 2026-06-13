package client

import (
	"encoding/json"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Geometry is the client's terminal geometry. Pixel fields are 0 when the
// terminal doesn't report them (many don't).
type Geometry struct {
	Rows   int `json:"rows"`
	Cols   int `json:"cols"`
	XPixel int `json:"xpixel"`
	YPixel int `json:"ypixel"`
}

func (g Geometry) valid() bool { return g.Rows > 0 && g.Cols > 0 }

// CurrentGeometry reads the live terminal geometry via TIOCGWINSZ, capturing
// pixel dimensions too. If the live read is unusable it falls back to the most
// recently cached geometry, then to a sane default. Any usable live read is
// written back to the cache so a later session (or an onward ssh hop to another
// box that shares the cache) can reuse it.
func CurrentGeometry() Geometry {
	if ws, err := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ); err == nil && ws != nil {
		g := Geometry{Rows: int(ws.Row), Cols: int(ws.Col), XPixel: int(ws.Xpixel), YPixel: int(ws.Ypixel)}
		if g.valid() {
			g.save()
			return g
		}
	}
	if g := loadGeometry(); g.valid() {
		return g
	}
	return Geometry{Rows: 24, Cols: 80}
}

func geometryPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "aethershell", "geometry.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aethershell", "geometry.json")
}

func (g Geometry) save() {
	p := geometryPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return
	}
	data, err := json.Marshal(g)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, data, 0600) == nil {
		os.Rename(tmp, p)
	}
}

func loadGeometry() Geometry {
	data, err := os.ReadFile(geometryPath())
	if err != nil {
		return Geometry{}
	}
	var g Geometry
	if json.Unmarshal(data, &g) != nil {
		return Geometry{}
	}
	return g
}
