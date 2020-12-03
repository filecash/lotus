package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/signal"
	"syscall"

	"bufio"
	"io/ioutil"
	"math/big"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alexflint/go-filemutex"
	"github.com/docker/go-units"
	"github.com/filecoin-project/go-address"
	paramfetch "github.com/filecoin-project/go-paramfetch"
	saproof "github.com/filecoin-project/specs-actors/actors/runtime/proof"
	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	"github.com/lingdor/stackerror"
	"github.com/mitchellh/go-homedir"
	"github.com/prometheus/common/log"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-state-types/abi"

	lcli "github.com/filecoin-project/lotus/cli"

	"github.com/filecoin-project/lotus/extern/sector-storage/ffiwrapper"

	"github.com/filecoin-project/lotus/extern/sector-storage/ffiwrapper/basicfs"

	"github.com/filecoin-project/lotus/build"
	"github.com/urfave/cli/v2"

	"github.com/filecoin-project/lotus/chain/actors/builtin/miner"
	"github.com/filecoin-project/lotus/chain/types"

	miner0 "github.com/filecoin-project/specs-actors/actors/builtin/miner"
	"github.com/filecoin-project/specs-storage/storage"
)

var c chan os.Signal

//var log = logging.Logger("filecash-check")

type Commit2In struct {
	SectorNum  int64
	Phase1Out  []byte
	SectorSize uint64
}

func main_exe() {

	logging.SetLogLevel("*", "INFO")

	log.Info("Starting filecash sectors check")

	miner0.SupportedProofTypes[abi.RegisteredSealProof_StackedDrg2KiBV1] = struct{}{}

	app := &cli.App{
		Name:    "filecash-sector-checker",
		Usage:   "check sector window post",
		Version: build.UserVersion(),
		Commands: []*cli.Command{
			proveCmd,
			sealBenchCmd,
			importBenchCmd,
		},
	}

	if err := app.Run(os.Args); err != nil {
		stackerror.New(err.Error())
		//log.Warnf("%+v", err)
		return
	}

}

var sealBenchCmd = &cli.Command{
	Name:  "checking",
	Usage: "filecash sector checker",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "storage-dir",
			Value: "~/.filecash-sector-checker",
			Usage: "Path to the storage directory that will store sectors long term",
		},
		&cli.StringFlag{
			Name:  "miner-id",
			Usage: "Enter the miner-id to be checked",
			Value: "t01000",
		},
		&cli.StringFlag{
			Name:  "sector-id-fliter",
			Usage: "Enter the sector-id to be checked (Empty will check all sectors)",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "sector-size",
			Value: "4GiB",
			Usage: "size of the sectors in bytes, i.e. 32GiB",
		},
		&cli.StringFlag{
			Name:  "sectors-file",
			Value: "/sectors.txt",
			Usage: "absolute path file (Empty will default path)",
		},
		&cli.BoolFlag{
			Name:  "no-gpu",
			Usage: "disable gpu usage for the checking",
		},
		&cli.IntFlag{
			Name:  "number",
			Value: 1,
		},
		&cli.StringFlag{
			Name:  "cidcommr",
			Usage: "CIDcommR,  eg/default. bagboea4b5abcbkyyzhl37s5kyjjegeysedpczhija7cczazapavjejbppck57b2z",
			Value: "",
		},
	},

	Action: func(c *cli.Context) error {
		if c.Bool("no-gpu") {
			err := os.Setenv("BELLMAN_NO_GPU", "1")
			if err != nil {
				stackerror.New(err.Error())
				return xerrors.Errorf("setting no-gpu flag: %w", err)
			}
		}

		var sbdir string
		// dis:=c.String("storage-dir")
		fmt.Print(c.String("storage-dir")+"/sealed/", "@@@@")
		sdir, err := homedir.Expand(c.String("storage-dir"))
		if err != nil {
			stackerror.New(err.Error())
			return err
		}

		err = os.MkdirAll(sdir, 0775) //nolint:gosec
		if err != nil {
			stackerror.New(err.Error())
			return xerrors.Errorf("creating sectorbuilder dir: %w", err)
		}

		defer func() {
		}()

		sbdir = sdir

		// miner address
		maddr, err := address.NewFromString(c.String("miner-id"))
		if err != nil {
			stackerror.New(err.Error())
			return err
		}

		//log.Info(dirs, c.String("sectors-file"), "path<<<<", "\n")
		if len(c.String("sector-id-fliter")) > 0 {
			//fmt.Print(c.String("storage-dir"), "<<<<<<<<<")
			FixSectorTXT(c.String("sector-id-fliter"), c.String("storage-dir"), c.String("sectors-file"))
		} else {
			//fmt.Print(c.String("storage-dir"), "<<<<<<<<<")
			FixSectorTXT("", c.String("storage-dir"), c.String("sectors-file"))

		}

		log.Info("miner maddr: ", maddr, " ")
		amid, err := address.IDFromAddress(maddr)
		if err != nil {
			stackerror.New(err.Error())
			return err
		}
		log.Info("miner amid: ", amid, " ")
		mid := abi.ActorID(amid)
		log.Info("miner mid: ", mid, " ")

		// sector size
		sectorSizeInt, err := units.RAMInBytes(c.String("sector-size"))
		if err != nil {
			stackerror.New(err.Error())
			return err
		}
		sectorSize := abi.SectorSize(sectorSizeInt)

		spt, err := miner.SealProofTypeFromSectorSize(sectorSize, build.NewestNetworkVersion)
		if err != nil {
			stackerror.New(err.Error())
			return err
		}

		if err := paramfetch.GetParams(lcli.ReqContext(c), build.ParametersJSON(), uint64(sectorSize)); err != nil {
			stackerror.New(err.Error())
			return xerrors.Errorf("getting params: %w", err)
		}

		sbfs := &basicfs.Provider{
			Root: sbdir,
		}

		sb, err := ffiwrapper.New(sbfs)
		if err != nil {
			stackerror.New(err.Error())
			return err
		}

		sealedSectors := getSectorsInfo(c.String("sectors-file"), spt)

		var challenge [32]byte
		rand.Read(challenge[:])

		log.Info("computing window post snark (cold)\n")
		log.Info("=================================================================================\n")

		ft := c.String("sector-id-fliter")
		if len(ft) > 0 {
			for _, i := range sealedSectors {

				for _, sectorfilter := range strings.Split(ft, ",") {

					if sectorfilter == i.SectorNumber.String() {

						li := []saproof.SectorInfo{i}
						wproof1, ps, err := sb.GenerateWindowPoSt(context.TODO(), mid, li, challenge[:])
						if err != nil {
							fmt.Print(err.Error(), " ##################### ")
							if len(ps) > 0 {
								fmt.Print("  Miner:")
								fmt.Print(ps[0].Miner)
								fmt.Print("  Number:")
								fmt.Print(ps[0].Number)
								fmt.Print("\n")
							}
						} else {
							if len(ps) > 0 {
								fmt.Print("  Miner:")
								fmt.Print(ps[0].Miner)
								fmt.Print("  Number:")
								fmt.Print(ps[0].Number)
								fmt.Print("\n")
							}
						}
						log.Info(" \n  mid: ", mid.String(), " \n  SectorID: ", i.SectorNumber, "\n  SealedCID: ", i.SealedCID.String(), " <<<<<")
						wpvi1 := saproof.WindowPoStVerifyInfo{
							Randomness:        challenge[:],
							Proofs:            wproof1,
							ChallengedSectors: li,
							Prover:            mid,
						}

						ok, err := ffiwrapper.ProofVerifier.VerifyWindowPoSt(context.TODO(), wpvi1)
						if err != nil {
							log.Error(err.Error())

						}
						if !ok {
							log.Error("post verification failed \n")
						} else {
							log.Info("post verification ok! \n")
						}
					}
				}
			}
		} else {
			for _, i := range sealedSectors {

				li := []saproof.SectorInfo{i}
				wproof1, ps, err := sb.GenerateWindowPoSt(context.TODO(), mid, li, challenge[:])
				if err != nil {
					fmt.Print(err.Error(), " ##################### ")
					if len(ps) > 0 {
						fmt.Print("  Miner:")
						fmt.Print(ps[0].Miner)
						fmt.Print("  Number:")
						fmt.Print(ps[0].Number)
						fmt.Print("\n")
					}

				} else {
					if len(ps) > 0 {
						fmt.Print("  Miner:")
						fmt.Print(ps[0].Miner)
						fmt.Print("  Number:")
						fmt.Print(ps[0].Number)
						fmt.Print("\n")
					}
				}
				log.Info(" \n mid:", mid.String(), " \n SectorID:", i.SectorNumber, " \n SealedCID: ", i.SealedCID.String(), "\n <<<<<")
				wpvi1 := saproof.WindowPoStVerifyInfo{
					Randomness:        challenge[:],
					Proofs:            wproof1,
					ChallengedSectors: li,
					Prover:            mid,
				}
				//log.Info("generate window PoSt skipped sectors", "sectors", ps, "error", err)

				ok, err := ffiwrapper.ProofVerifier.VerifyWindowPoSt(context.TODO(), wpvi1)
				if err != nil {
					log.Error(err.Error())

				}
				if !ok {
					log.Error("post verification failed \n")
				} else {
					log.Info("post verification ok! \n")
				}
			}
		}

		log.Info("=================================================================================\n")

		return nil
	},
}

func getSectorsInfo(filePath string, proofType abi.RegisteredSealProof) []saproof.SectorInfo {

	sealedSectors := make([]saproof.SectorInfo, 0)

	file, err := os.Open(filePath)
	defer file.Close()
	if err != nil {
		stackerror.New(err.Error())
		return sealedSectors
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		sectorIndex := scanner.Text()

		index, error := strconv.Atoi(sectorIndex)
		if error != nil {
			stackerror.New(error.Error())
			fmt.Println("error")
			break
		}

		scanner.Scan()
		cidStr := scanner.Text()
		ccid, err := cid.Decode(cidStr)
		if err != nil {
			log.Infof("cid error, ignore sectors after this: %d, %s", uint64(index), err)
			return sealedSectors
		}

		var sector saproof.SectorInfo
		sector.SealProof = proofType
		sector.SectorNumber = abi.SectorNumber(uint64(index))
		sector.SealedCID = ccid

		sealedSectors = append(sealedSectors, sector)

		log.Infof("id: ", sector.SectorNumber)
		log.Infof("cid: ", sector.SealedCID)

	}

	fmt.Println("sector number", len(sealedSectors))
	return sealedSectors
}

type ParCfg struct {
	PreCommit1 int
	PreCommit2 int
	Commit     int
}

var proveCmd = &cli.Command{
	Name:  "prove",
	Usage: "filecash check proof",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "no-gpu",
			Usage: "disable gpu usage for the benchmark run",
		},
		&cli.StringFlag{
			Name:  "miner-id",
			Usage: "pass miner address (only necessary if using existing sectorbuilder)",
			Value: "t01000",
		},
		&cli.StringFlag{
			Name:  "sector-id-fliter",
			Usage: "pass miner address (only necessary if using existing sectorbuilder)",
			Value: "",
		},
	},
	Action: func(c *cli.Context) error {
		if c.Bool("no-gpu") {
			err := os.Setenv("BELLMAN_NO_GPU", "1")
			if err != nil {

				return stackerror.New(err.Error())
			}
		}

		if !c.Args().Present() {
			return xerrors.Errorf("Usage: filecash sectors check prove [input.json]")
		}

		inb, err := ioutil.ReadFile(c.Args().First())
		if err != nil {
			return stackerror.New(err.Error())
			//return xerrors.Errorf("reading input file: %w", err)
		}

		var c2in Commit2In
		if err := json.Unmarshal(inb, &c2in); err != nil {
			return stackerror.New(err.Error())
			//return xerrors.Errorf("unmarshalling input file: %w", err)
		}

		if err := paramfetch.GetParams(lcli.ReqContext(c), build.ParametersJSON(), c2in.SectorSize); err != nil {
			return stackerror.New(err.Error())
			//return xerrors.Errorf("getting params: %w", err)
		}

		maddr, err := address.NewFromString(c.String("miner-id"))
		if err != nil {
			stackerror.New(err.Error())
			return err
		}
		mid, err := address.IDFromAddress(maddr)
		if err != nil {
			stackerror.New(err.Error())
			return err
		}

		ft := c.String("sector-id-fliter")
		if len(ft) > 0 {
			for _, sectorfilter := range strings.Split(ft, ",") {
				cid, err := strconv.Atoi(sectorfilter)
				if err != nil {
					panic(err)
				}
				fmt.Print("^^^^^^^^^^^^")
				if cid == int(c2in.SectorNum) {
					fmt.Print("22222^^^^^^^^^^^^")
					spt, err := miner.SealProofTypeFromSectorSize(abi.SectorSize(c2in.SectorSize), build.NewestNetworkVersion)
					if err != nil {
						stackerror.New(err.Error())
						return err
					}

					sb, err := ffiwrapper.New(nil)
					if err != nil {
						stackerror.New(err.Error())
						return err
					}

					start := time.Now()
					//abi.SectorID{Miner: abi.ActorID(mid)
					sector := storage.SectorRef{ID: abi.SectorID{Miner: abi.ActorID(mid)}, ProofType: spt}

					proof, err := sb.SealCommit2(context.TODO(), sector, c2in.Phase1Out)
					if err != nil {
						stackerror.New(err.Error())
						return err
					}

					sealCommit2 := time.Now()

					fmt.Printf("proof: %x\n", proof)

					fmt.Printf("----\nresults (v27) (%d)\n", c2in.SectorSize)
					dur := sealCommit2.Sub(start)

					fmt.Printf("seal: commit phase 2: %s (%s)\n", dur, bps(abi.SectorSize(c2in.SectorSize), dur))
					return nil
				}
			}
			return nil
		} else {

			fmt.Print("9999^^^^^^^^^^^^")
			spt, err := miner.SealProofTypeFromSectorSize(abi.SectorSize(c2in.SectorSize), build.NewestNetworkVersion)
			if err != nil {
				stackerror.New(err.Error())
				return err
			}

			sb, err := ffiwrapper.New(nil)
			if err != nil {
				stackerror.New(err.Error())
				return err
			}

			start := time.Now()

			sector := storage.SectorRef{ID: abi.SectorID{Miner: abi.ActorID(mid)}, ProofType: spt}

			proof, err := sb.SealCommit2(context.TODO(), sector, c2in.Phase1Out)
			if err != nil {
				stackerror.New(err.Error())
				return err
			}

			sealCommit2 := time.Now()

			fmt.Printf("proof: %x\n", proof)

			fmt.Printf("----\nresults (v27) (%d)\n", c2in.SectorSize)
			dur := sealCommit2.Sub(start)

			fmt.Printf("seal: commit phase 2: %s (%s)\n", dur, bps(abi.SectorSize(c2in.SectorSize), dur))
			return nil
		}
		return nil
	},
}

func bps(data abi.SectorSize, d time.Duration) string {
	bdata := new(big.Int).SetUint64(uint64(data))
	bdata = bdata.Mul(bdata, big.NewInt(time.Second.Nanoseconds()))
	bps := bdata.Div(bdata, big.NewInt(d.Nanoseconds()))
	return types.SizeStr(types.BigInt{Int: bps}) + "/s"
}

func main() {
	c := make(chan os.Signal)
	m, err := filemutex.New("/tmp/foo.lock")

	if err != nil {
		log.Error("Directory did not exist or file could not created")
	}
	if m.TryLock() != nil {
		log.Error("You cannot start two processes simultaneously")
		os.Exit(0)
	}

	// ctrl+c kill
	signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for s := range c {
			switch s {
			case syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT:
				fmt.Println(s)
				m.Unlock()
				ExitFunc()

			case syscall.SIGUSR1:
				fmt.Println(s)
				m.Unlock()
				ExitFunc()

			case syscall.SIGUSR2:
				fmt.Println(s)
				m.Unlock()
				ExitFunc()

			default:
				m.Unlock()
				ExitFunc()
				fmt.Println("other", s)

			}
		}
	}()

	m.Lock()
	main_exe()
	m.Unlock()
}

func ExitFunc() {
	os.Exit(0)
}
