package subcommands

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"
	"github.com/bedrock-tool/bedrocktool/locale"
	"github.com/bedrock-tool/bedrocktool/utils"

	"github.com/google/subcommands"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"github.com/sirupsen/logrus"
)

func init() {
	utils.RegisterCommand(&CaptureCMD{})
}

var dumpLock sync.Mutex

func dumpPacket(f io.WriteCloser, toServer bool, payload []byte) {
	dumpLock.Lock()
	defer dumpLock.Unlock()
	f.Write([]byte{0xAA, 0xAA, 0xAA, 0xAA})
	packetSize := uint32(len(payload))
	binary.Write(f, binary.LittleEndian, packetSize)
	binary.Write(f, binary.LittleEndian, toServer)
	binary.Write(f, binary.LittleEndian, time.Now().UnixMilli())
	n, err := f.Write(payload)
	if err != nil {
		logrus.Error(err)
	}
	if n < int(packetSize) {
		f.Write(make([]byte, int(packetSize)-n))
	}
	f.Write([]byte{0xBB, 0xBB, 0xBB, 0xBB})
}

type CaptureCMD struct {
	serverAddress      string
	pathCustomUserData string
}

func (*CaptureCMD) Name() string     { return "capture" }
func (*CaptureCMD) Synopsis() string { return locale.Loc("capture_synopsis", nil) }

func (c *CaptureCMD) SetFlags(f *flag.FlagSet) {
	f.StringVar(&c.serverAddress, "address", "", "remote server address")
	f.StringVar(&c.pathCustomUserData, "userdata", "", locale.Loc("custom_user_data", nil))
}

func (c *CaptureCMD) SettingsUI() *widget.Form {
	return widget.NewForm(
		widget.NewFormItem(
			"serverAddress", widget.NewEntryWithData(binding.BindString(&c.serverAddress)),
		), widget.NewFormItem(
			"pathCustomUserData", widget.NewEntryWithData(binding.BindString(&c.pathCustomUserData)),
		),
	)
}

func (c *CaptureCMD) MainWindow() error {
	return nil
}

func (c *CaptureCMD) Usage() string {
	return c.Name() + ": " + c.Synopsis() + "\n" + locale.Loc("server_address_help", nil)
}

func (c *CaptureCMD) Execute(ctx context.Context, f *flag.FlagSet, _ ...interface{}) subcommands.ExitStatus {
	address, hostname, err := utils.ServerInput(ctx, c.serverAddress)
	if err != nil {
		logrus.Fatal(err)
		return 1
	}

	os.Mkdir("captures", 0o775)
	fio, err := os.Create("captures/" + hostname + "-" + time.Now().Format("2006-01-02_15-04-05") + ".pcap2")
	if err != nil {
		logrus.Fatal(err)
		return 1
	}
	defer fio.Close()
	utils.WriteReplayHeader(fio)

	proxy, err := utils.NewProxy(c.pathCustomUserData)
	if err != nil {
		logrus.Fatal(err)
	}
	proxy.PacketFunc = func(header packet.Header, payload []byte, src, dst net.Addr) {
		IsfromClient := src.String() == proxy.Client.LocalAddr().String()

		buf := bytes.NewBuffer(nil)
		header.Write(buf)
		buf.Write(payload)
		dumpPacket(fio, IsfromClient, buf.Bytes())
	}

	err = proxy.Run(ctx, address)
	time.Sleep(2 * time.Second)
	if err != nil {
		logrus.Fatal(err)
		return 1
	}
	return 0
}
