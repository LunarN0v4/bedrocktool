package utils

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"sort"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/resource"
)

type Pack interface {
	io.ReaderAt
	ReadAll() ([]byte, error)
	Encrypted() bool
	CanDecrypt() bool
	UUID() string
	Name() string
	Version() string
	ContentKey() string
	Len() int
	Manifest() resource.Manifest
	Base() *resource.Pack
	FS() (fs.FS, []string, error)
}

type Packb struct {
	*resource.Pack
	d bool
}

func (p *Packb) ReadAll() ([]byte, error) {
	buf := make([]byte, p.Len())
	off := 0
	for {
		n, err := p.ReadAt(buf[off:], int64(off))
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		off += n
	}
	return buf, nil
}

func (p *Packb) CanDecrypt() bool {
	return false
}

func (p *Packb) SetD() {
	p.d = true
}

func (p *Packb) FS() (fs.FS, []string, error) {
	if p.Encrypted() && !p.d {
		return nil, nil, errors.New("encrypted")
	}
	r, err := zip.NewReader(p, int64(p.Len()))
	var names []string
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		names = append(names, f.Name)
	}
	sort.Strings(names)

	return r, names, err
}

func (p *Packb) Base() *resource.Pack {
	return p.Pack
}

var PackFromBase = func(pack *resource.Pack) Pack {
	b := &Packb{pack, false}
	return b
}

func GetPacks(server minecraft.IConn) (packs []Pack) {
	for _, pack := range server.ResourcePacks() {
		pack := PackFromBase(pack)
		packs = append(packs, pack)
	}
	return
}
