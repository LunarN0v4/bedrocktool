package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"fyne.io/fyne/v2/widget"
	"github.com/bedrock-tool/bedrocktool/gui"
	"github.com/bedrock-tool/bedrocktool/locale"
	"github.com/bedrock-tool/bedrocktool/utils"
	"github.com/bedrock-tool/bedrocktool/utils/crypt"
	"gopkg.in/square/go-jose.v2/json"

	_ "github.com/bedrock-tool/bedrocktool/subcommands"
	_ "github.com/bedrock-tool/bedrocktool/subcommands/skins"
	_ "github.com/bedrock-tool/bedrocktool/subcommands/world"

	"github.com/google/subcommands"
	"github.com/sirupsen/logrus"
)

type CLI struct {
	gui.UI
}

func (c *CLI) Init() {
}

func (c *CLI) SetOptions() {
	flag.Parse()
}

func (c *CLI) Execute(ctx context.Context) error {
	subcommands.Execute(ctx)
	return nil
}

func main() {
	defer func() {
		if err := recover(); err != nil {
			logrus.Errorf(locale.Loc("fatal_error", nil))
			println("")
			println("--COPY FROM HERE--")
			logrus.Infof("Version: %s", utils.Version)
			logrus.Infof("Cmdline: %s", os.Args)
			logrus.Errorf("Error: %s", err)
			println("stacktrace from panic: \n" + string(debug.Stack()))
			println("--END COPY HERE--")
			println("")
			println(locale.Loc("report_issue", nil))
			if utils.Options.ExtraDebug {
				println(locale.Loc("used_extra_debug_report", nil))
			}
			if utils.Options.IsInteractive {
				input := bufio.NewScanner(os.Stdin)
				input.Scan()
			}
			os.Exit(1)
		}
	}()

	logrus.SetLevel(logrus.DebugLevel)
	if utils.Version != "" {
		logrus.Infof(locale.Loc("bedrocktool_version", locale.Strmap{"Version": utils.Version}))
	}

	newVersion, err := utils.Updater.UpdateAvailable()
	if err != nil {
		logrus.Error(err)
	}

	if newVersion != "" && utils.Version != "" {
		logrus.Infof(locale.Loc("update_available", locale.Strmap{"Version": newVersion}))
	}

	ctx, cancel := context.WithCancel(context.Background())

	flag.StringVar(&utils.RealmsEnv, "realms-env", "", "realms env")
	flag.BoolVar(&utils.Options.Debug, "debug", false, locale.Loc("debug_mode", nil))
	flag.BoolVar(&utils.Options.Preload, "preload", false, locale.Loc("preload_packs", nil))
	flag.BoolVar(&utils.Options.ExtraDebug, "extra-debug", false, locale.Loc("extra_debug", nil))
	flag.String("lang", "", "lang")
	flag.BoolVar(&utils.Options.EnableDNS, "dns", false, locale.Loc("enable_dns", nil))

	subcommands.Register(subcommands.HelpCommand(), "")
	subcommands.ImportantFlag("debug")
	subcommands.ImportantFlag("dns")
	subcommands.ImportantFlag("preload")
	subcommands.HelpCommand()

	var ui gui.UI

	if len(os.Args) < 2 {
		ui = gui.NewGUI()
		utils.Options.IsInteractive = true
	} else {
		ui = &CLI{}
	}

	ui.Init()
	ui.SetOptions()

	if utils.Options.EnableDNS {
		utils.InitDNS()
	}

	if utils.Options.ExtraDebug {
		utils.Options.Debug = true

		var logPlain, logCryptEnc io.WriteCloser = nil, nil

		// open plain text log
		logPlain, err = os.Create("packets.log")
		if err != nil {
			logrus.Error(err)
		} else {
			defer logPlain.Close()
		}

		// open gpg log
		logCrypt, err := os.Create("packets.log.gpg")
		if err != nil {
			logrus.Error(err)
		} else {
			defer logCrypt.Close()
			// encrypter for the log
			logCryptEnc, err = crypt.Encer("packets.log", logCrypt)
			if err != nil {
				logrus.Error(err)
			} else {
				defer logCryptEnc.Close()
			}
		}

		utils.FLog = io.MultiWriter(logPlain, logCryptEnc)
		if err != nil {
			logrus.Error(err)
		}
	}

	// exit cleanup
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		println("cancelling")
		cancel()
	}()

	ui.Execute(ctx)

	if utils.Options.IsInteractive {
		logrus.Info(locale.Loc("enter_to_exit", nil))
		input := bufio.NewScanner(os.Stdin)
		input.Scan()
	}
}

type TransCMD struct {
	auth bool
}

func (*TransCMD) Name() string     { return "trans" }
func (*TransCMD) Synopsis() string { return "" }

func (c *TransCMD) SetFlags(f *flag.FlagSet) {
	f.BoolVar(&c.auth, "auth", false, locale.Loc("should_login_xbox", nil))
}

func (c *TransCMD) Usage() string {
	return c.Name() + ": " + c.Synopsis() + "\n"
}

func (c *TransCMD) Execute(_ context.Context, f *flag.FlagSet, _ ...interface{}) subcommands.ExitStatus {
	const (
		BlackFg = "\033[30m"
		Bold    = "\033[1m"
		Blue    = "\033[46m"
		Pink    = "\033[45m"
		White   = "\033[47m"
		Reset   = "\033[0m"
	)
	if c.auth {
		utils.GetTokenSource()
	}
	fmt.Println(BlackFg + Bold + Blue + " Trans " + Pink + " Rights " + White + " Are " + Pink + " Human " + Blue + " Rights " + Reset)
	return 0
}

type CreateCustomDataCMD struct {
	path string
}

func (*CreateCustomDataCMD) Name() string     { return "create-customdata" }
func (*CreateCustomDataCMD) Synopsis() string { return "" }

func (c *CreateCustomDataCMD) SetFlags(f *flag.FlagSet) {
	f.StringVar(&c.path, "path", "customdata.json", "where to save")
}

func (c *CreateCustomDataCMD) Usage() string {
	return c.Name() + ": " + c.Synopsis() + "\n"
}

func (c *CreateCustomDataCMD) Execute(_ context.Context, f *flag.FlagSet, _ ...interface{}) subcommands.ExitStatus {
	var data utils.CustomClientData
	fio, err := os.Create(c.path)
	if err == nil {
		defer fio.Close()
		var bdata []byte
		bdata, err = json.MarshalIndent(&data, "", "\t")
		fio.Write(bdata)
	}
	if err != nil {
		logrus.Error(err)
		return 1
	}
	return 0
}

func (c *TransCMD) SettingsUI() *widget.Form {
	return nil
}

func (c *TransCMD) MainWindow() error {
	return nil
}

func (c *CreateCustomDataCMD) SettingsUI() *widget.Form {
	return nil
}

func (c *CreateCustomDataCMD) MainWindow() error {
	return nil
}

func init() {
	utils.RegisterCommand(&TransCMD{})
	utils.RegisterCommand(&CreateCustomDataCMD{})
}
