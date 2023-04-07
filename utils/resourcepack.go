package utils

import (
	"errors"
	"io"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/resource"
)

type Pack interface {
	io.ReaderAt
	ReadAll() ([]byte, error)
	Decrypt() ([]byte, error)
	Encrypted() bool
	CanDecrypt() bool
	UUID() string
	Name() string
	Version() string
	ContentKey() string
	Len() int
	Manifest() resource.Manifest
	Base() *resource.Pack
}

type Packb struct {
	*resource.Pack
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

func (p *Packb) Decrypt() ([]byte, error) {
	return nil, errors.New("no_decrypt")
}

func (p *Packb) Base() *resource.Pack {
	return p.Pack
}

var PackFromBase = func(pack *resource.Pack) Pack {
	b := &Packb{pack}
	return b
}

func GetPacks(server *minecraft.Conn) (packs []Pack, err error) {
	for _, pack := range server.ResourcePacks() {
		pack := PackFromBase(pack)
		if pack.Encrypted() && pack.CanDecrypt() {
			data, err := pack.Decrypt()
			if err != nil {
				return nil, err
			}
			pack2, err := resource.FromBytes(data)
			if err != nil {
				return nil, err
			}
			packs = append(packs, &Packb{pack2})
		} else {
			packs = append(packs, pack)
		}
	}
	return
}
