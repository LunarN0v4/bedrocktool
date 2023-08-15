package proxy

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"sync"
	"time"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"github.com/sandertv/gophertunnel/minecraft/resource"
	"github.com/sirupsen/logrus"
)

type replayHeader struct {
	Version int32
}

var replayMagic = []byte("BTCP")

const (
	currentReplayVersion = 3
)

func WriteReplayHeader(f io.Writer) {
	f.Write(replayMagic)
	header := replayHeader{
		Version: currentReplayVersion,
	}
	binary.Write(f, binary.LittleEndian, &header)
}

type replayConnector struct {
	f       *os.File
	z       *zip.Reader
	packetF fs.File
	ver     uint32

	packets chan packet.Packet
	spawn   chan struct{}
	close   chan struct{}
	once    sync.Once

	pool       packet.Pool
	proto      minecraft.Protocol
	clientData login.ClientData

	gameData minecraft.GameData

	packetFunc PacketFunc

	resourcePackHandler *rpHandler
}

func (r *replayConnector) readPacket() (payload []byte, toServer bool, err error) {
	var magic uint32 = 0
	var packetLength uint32 = 0
	timeReceived := time.Now()

	err = binary.Read(r.packetF, binary.LittleEndian, &magic)
	if err != nil {
		if errors.Is(err, io.EOF) {
			logrus.Info("Reached End")
			return nil, false, nil
		}
		return nil, false, err
	}
	if magic != 0xAAAAAAAA {
		return nil, toServer, fmt.Errorf("wrong Magic")
	}
	binary.Read(r.packetF, binary.LittleEndian, &packetLength)
	binary.Read(r.packetF, binary.LittleEndian, &toServer)
	if r.ver >= 2 {
		var timeMs int64
		binary.Read(r.packetF, binary.LittleEndian, &timeMs)
		timeReceived = time.UnixMilli(timeMs)
	}

	payload = make([]byte, packetLength)
	n, err := io.ReadFull(r.packetF, payload)
	if err != nil {
		return nil, toServer, err
	}
	if n != int(packetLength) {
		return nil, toServer, fmt.Errorf("truncated")
	}

	var magic2 uint32
	binary.Read(r.packetF, binary.LittleEndian, &magic2)
	if magic2 != 0xBBBBBBBB {
		return nil, toServer, fmt.Errorf("wrong Magic2")
	}

	_ = timeReceived
	return payload, toServer, nil
}

func (r *replayConnector) handleLoginSequence(pk packet.Packet) (bool, error) {
	switch pk := pk.(type) {
	case *packet.StartGame:
		r.SetGameData(minecraft.GameData{
			WorldName:                    pk.WorldName,
			WorldSeed:                    pk.WorldSeed,
			Difficulty:                   pk.Difficulty,
			EntityUniqueID:               pk.EntityUniqueID,
			EntityRuntimeID:              pk.EntityRuntimeID,
			PlayerGameMode:               pk.PlayerGameMode,
			PersonaDisabled:              pk.PersonaDisabled,
			CustomSkinsDisabled:          pk.CustomSkinsDisabled,
			BaseGameVersion:              pk.BaseGameVersion,
			PlayerPosition:               pk.PlayerPosition,
			Pitch:                        pk.Pitch,
			Yaw:                          pk.Yaw,
			Dimension:                    pk.Dimension,
			WorldSpawn:                   pk.WorldSpawn,
			EditorWorld:                  pk.EditorWorld,
			WorldGameMode:                pk.WorldGameMode,
			GameRules:                    pk.GameRules,
			Time:                         pk.Time,
			ServerBlockStateChecksum:     pk.ServerBlockStateChecksum,
			CustomBlocks:                 pk.Blocks,
			Items:                        pk.Items,
			PlayerMovementSettings:       pk.PlayerMovementSettings,
			ServerAuthoritativeInventory: pk.ServerAuthoritativeInventory,
			Experiments:                  pk.Experiments,
			ClientSideGeneration:         pk.ClientSideGeneration,
			ChatRestrictionLevel:         pk.ChatRestrictionLevel,
			DisablePlayerInteractions:    pk.DisablePlayerInteractions,
		})

	case *packet.ResourcePacksInfo:
		return false, r.resourcePackHandler.OnResourcePacksInfo(pk)
	case *packet.ResourcePackDataInfo:
		return false, r.resourcePackHandler.OnResourcePackDataInfo(pk)
	case *packet.ResourcePackChunkData:
		return false, r.resourcePackHandler.OnResourcePackChunkData(pk)
	case *packet.ResourcePackStack:
		return false, r.resourcePackHandler.OnResourcePackStack(pk)

	case *packet.SetLocalPlayerAsInitialised:
		if pk.EntityRuntimeID != r.gameData.EntityRuntimeID {
			return false, fmt.Errorf("entity runtime ID mismatch: entity runtime ID in StartGame and SetLocalPlayerAsInitialised packets should be equal")
		}
		close(r.spawn)
		return true, nil
	}
	return false, nil
}

func (r *replayConnector) loop() {
	gameStarted := false
	defer r.Close()
	for {
		payload, toServer, err := r.readPacket()
		if err != nil {
			logrus.Error(err)
		}
		if payload == nil {
			return
		}
		var src, dst = r.RemoteAddr(), r.LocalAddr()
		if toServer {
			src, dst = r.LocalAddr(), r.RemoteAddr()
		}

		pkData, err := minecraft.ParseData(payload, r, src, dst)
		if err != nil {
			logrus.Error(err)
			return
		}
		pks, err := pkData.Decode(r)
		if err != nil {
			logrus.Error(err)
			continue
		}
		for _, pk := range pks {
			if !gameStarted {
				gameStarted, err = r.handleLoginSequence(pk)
				if err != nil {
					logrus.Error(err)
					return
				}
			} else {
				r.packets <- pk
			}
		}
	}
}

func createReplayConnector(filename string, packetFunc PacketFunc) (r *replayConnector, err error) {
	r = &replayConnector{
		pool:       minecraft.DefaultProtocol.Packets(true),
		proto:      minecraft.DefaultProtocol,
		packetFunc: packetFunc,
		spawn:      make(chan struct{}),
		close:      make(chan struct{}),
		packets:    make(chan packet.Packet),
	}
	r.resourcePackHandler = NewRpHandler(r, nil)
	cache := &replayCache{}
	r.resourcePackHandler.cache = cache

	logrus.Infof("Reading replay %s", filename)

	// open zip
	r.f, err = os.Open(filename)
	if err != nil {
		return nil, err
	}
	s, _ := r.f.Stat()
	r.z, err = zip.NewReader(r.f, s.Size())
	if err != nil {
		return nil, err
	}

	f, err := r.z.Open("version")
	if err != nil {
		return nil, err
	}
	binary.Read(f, binary.LittleEndian, &r.ver)
	if r.ver != 3 {
		return nil, errors.New("wrong version")
	}

	// read all packs
	err = cache.ReadFrom(r.z)
	if err != nil {
		return nil, err
	}

	// open packets bin
	r.packetF, err = r.z.Open("packets.bin")
	if err != nil {
		return nil, err
	}

	go r.loop()
	return r, nil
}

func (r *replayConnector) DisconnectOnInvalidPacket() bool {
	return false
}

func (r *replayConnector) DisconnectOnUnknownPacket() bool {
	return false
}

func (r *replayConnector) OnDisconnect() <-chan struct{} {
	return r.close
}

func (r *replayConnector) Expect(...uint32) {
}

func (r *replayConnector) SetLoggedIn() {

}

func (r *replayConnector) Close() error {
	r.once.Do(func() {
		close(r.close)
		close(r.packets)
	})
	return nil
}

func (r *replayConnector) Authenticated() bool {
	return true
}

func (r *replayConnector) ChunkRadius() int {
	return 80
}

func (r *replayConnector) ClientCacheEnabled() bool {
	return false
}

func (r *replayConnector) ClientData() login.ClientData {
	return r.clientData
}

func (r *replayConnector) DoSpawn() error {
	return r.DoSpawnContext(context.Background())
}

func (r *replayConnector) DoSpawnContext(ctx context.Context) error {
	select {
	case <-r.close:
		return errors.New("do spawn")
	case <-ctx.Done():
		return errors.New("do spawn")
	case <-r.spawn:
		// Conn was spawned successfully.
		return nil
	}
}

func (r *replayConnector) DoSpawnTimeout(timeout time.Duration) error {
	c, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.DoSpawnContext(c)
}

func (r *replayConnector) Flush() error {
	return nil
}

func (r *replayConnector) GameData() minecraft.GameData {
	return r.gameData
}

func (r *replayConnector) IdentityData() login.IdentityData {
	return login.IdentityData{}
}

func (r *replayConnector) Latency() time.Duration {
	return 0
}

func (r *replayConnector) LocalAddr() net.Addr {
	return &net.UDPAddr{
		IP: net.IPv4(1, 1, 1, 1),
	}
}

func (r *replayConnector) Read(b []byte) (n int, err error) {
	return 0, errors.New("not Implemented")
}

func (r *replayConnector) ReadPacket() (pk packet.Packet, err error) {
	select {
	case <-r.close:
		return nil, net.ErrClosed
	case p, ok := <-r.packets:
		if !ok {
			err = net.ErrClosed
		}
		return p, err
	}
}

func (r *replayConnector) Write(b []byte) (n int, err error) {
	return 0, errors.New("not Implemented")
}

func (r *replayConnector) WritePacket(pk packet.Packet) error {
	return nil
}

func (r *replayConnector) RemoteAddr() net.Addr {
	return &net.UDPAddr{
		IP: net.IPv4(2, 2, 2, 2),
	}
}

func (r *replayConnector) ResourcePacks() []*resource.Pack {
	return r.resourcePackHandler.ResourcePacks()
}

func (r *replayConnector) SetGameData(data minecraft.GameData) {
	r.gameData = data
}

func (r *replayConnector) StartGame(data minecraft.GameData) error {
	return r.StartGameContext(context.Background(), data)
}

func (r *replayConnector) StartGameContext(ctx context.Context, data minecraft.GameData) error {
	return nil
}

func (r *replayConnector) StartGameTimeout(data minecraft.GameData, timeout time.Duration) error {
	c, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.StartGameContext(c, data)
}

func (r *replayConnector) SetDeadline(t time.Time) error {
	return nil
}

func (r *replayConnector) SetReadDeadline(t time.Time) error {
	return nil
}

func (r *replayConnector) SetWriteDeadline(time.Time) error {
	return nil
}

func (r *replayConnector) Pool() packet.Pool {
	return r.pool
}

func (r *replayConnector) ShieldID() int32 {
	return 0
}

func (r *replayConnector) Proto() minecraft.Protocol {
	return r.proto
}

func (r *replayConnector) PacketFunc(header packet.Header, payload []byte, src, dst net.Addr) {
	if r.packetFunc != nil {
		r.packetFunc(header, payload, src, dst)
	}
}