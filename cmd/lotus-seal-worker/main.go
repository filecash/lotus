package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/ipfs/go-datastore/namespace"
	logging "github.com/ipfs/go-log/v2"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/urfave/cli/v2"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/go-jsonrpc/auth"
	paramfetch "github.com/filecoin-project/go-paramfetch"
	"github.com/filecoin-project/go-statestore"

	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/api/apistruct"
	"github.com/filecoin-project/lotus/build"
	lcli "github.com/filecoin-project/lotus/cli"
	sectorstorage "github.com/filecoin-project/lotus/extern/sector-storage"
	"github.com/filecoin-project/lotus/extern/sector-storage/sealtasks"
	"github.com/filecoin-project/lotus/extern/sector-storage/stores"
	"github.com/filecoin-project/lotus/lib/lotuslog"
	"github.com/filecoin-project/lotus/lib/rpcenc"
	"github.com/filecoin-project/lotus/metrics"
	"github.com/filecoin-project/lotus/node/modules"
	"github.com/filecoin-project/lotus/node/repo"
)

var log = logging.Logger("main")

const FlagWorkerRepo = "worker-repo"

// TODO remove after deprecation period
const FlagWorkerRepoDeprecation = "workerrepo"

func main() {
	build.RunningNodeType = build.NodeWorker

	lotuslog.SetupLogLevels()

	local := []*cli.Command{
		runCmd,
		//autoTaskCmd,
		infoCmd,
		storageCmd,
		setCmd,
		waitQuietCmd,
		tasksCmd,
	}

	app := &cli.App{
		Name:    "lotus-worker",
		Usage:   "Remote miner worker",
		Version: build.UserVersion(),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    FlagWorkerRepo,
				Aliases: []string{FlagWorkerRepoDeprecation},
				EnvVars: []string{"LOTUS_WORKER_PATH", "WORKER_PATH"},
				Value:   "~/.lotusworker", // TODO: Consider XDG_DATA_HOME
				Usage:   fmt.Sprintf("Specify worker repo path. flag %s and env WORKER_PATH are DEPRECATION, will REMOVE SOON", FlagWorkerRepoDeprecation),
			},
			&cli.StringFlag{
				Name:    "miner-repo",
				Aliases: []string{"storagerepo"},
				EnvVars: []string{"LOTUS_MINER_PATH", "LOTUS_STORAGE_PATH"},
				Value:   "~/.lotusminer", // TODO: Consider XDG_DATA_HOME
				Usage:   fmt.Sprintf("Specify miner repo path. flag storagerepo and env LOTUS_STORAGE_PATH are DEPRECATION, will REMOVE SOON"),
			},
			&cli.BoolFlag{
				Name:  "enable-gpu-proving",
				Usage: "enable use of GPU for mining operations",
				Value: true,
			},
		},

		Commands: local,
	}
	app.Setup()
	app.Metadata["repoType"] = repo.Worker

	if err := app.Run(os.Args); err != nil {
		log.Warnf("%+v", err)
		return
	}
}

// var autoTaskCmd = &cli.Command{
// 	Name:  "autoTask",
// 	Usage: "Start auto task worker",
// 	Flags: []cli.Flag{
// 		&cli.Int64Flag{
// 			Name: "app1",
// 			Usage: "auto task ap count",
// 			Value: 0,
// 		},
// 		&cli.Int64Flag{
// 			Name: "p2",
// 			Usage: "auto task p2 count",
// 			Value: 0,
// 		},
// 		&cli.Int64Flag{
// 			Name: "c1",
// 			Usage: "auto task c1 count",
// 			Value: 0,
// 		},
// 		&cli.Int64Flag{
// 			Name: "c2",
// 			Usage: "auto task c2 count",
// 			Value: 0,
// 		},
// 		&cli.Uint64Flag{
// 			Name: "balance",
// 			Usage: "auto task balance",
// 			Value: 0,
// 		},
// 		&cli.Int64Flag{
// 			Name: "delay",
// 			Usage: "auto task delay (Seconds)",
// 			Value: 180,
// 		},
// 	},
// 	Action: func(cctx *cli.Context) error {
// 		log.Info("Starting auto task worker")
// 		delay := cctx.Int64("delay")
// 		if delay < 30 {
// 			return xerrors.Errorf("Delay cannot be less than 30 seconds")
// 		}
// 		limitList := make(map[string]int64)

// 		if app1 := cctx.Int64("app1"); app1 != 0 {
// 			limitList["app1"] = app1
// 		}
// 		if p2 := cctx.Int64("p2"); p2 != 0 {
// 			limitList["p2"] = p2
// 		}
// 		if c1 := cctx.Int64("c1"); c1 != 0 {
// 			limitList["c1"] = c1
// 		}
// 		if c2 := cctx.Int64("c1"); c2 != 0 {
// 			limitList["c2"] = c2
// 		}

// 		autoTaskBalance := types.BigMul(types.NewInt(cctx.Uint64("balance")), types.NewInt(build.FilecoinPrecision))

// 		ctx := lcli.ReqContext(cctx)

// 		nodeApi, nodeApiCloser, err := lcli.GetStorageMinerAPI(cctx)
// 		if err != nil {
// 			return err
// 		}
// 		maddr, err := nodeApi.ActorAddress(ctx)
// 		if err != nil {
// 			nodeApiCloser()
// 			return err
// 		}
// 		nodeApiCloser()

// 		lotusApi, lotusCloser, err := lcli.GetFullNodeAPI(cctx)
// 		if err != nil {
// 			return err
// 		}

// 		mInfo, err := lotusApi.StateMinerInfo(ctx, maddr, types.EmptyTSK)
// 		if err != nil {
// 			lotusCloser()
// 			return err
// 		}
// 		lotusCloser()

// 		api, Closer, err := lcli.GetWorkerAPI(cctx)
// 		if err != nil {
// 			return err
// 		}

// 		if err := api.AddAutoTaskLimit(ctx, limitList); err != nil {
// 			Closer()
// 			return err
// 		}
// 		Closer()

// 		sigs := make(chan os.Signal,1)
// 		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

// 		timer := time.NewTicker(time.Duration(delay) * time.Second)
// 		for {

// 			select {
// 			case <-timer.C:
// 				log.Info("Starting auto task worker")
// 				wapi, workerApiCloser, err := lcli.GetWorkerAPI(cctx)
// 				if err != nil {
// 					log.Errorf("auto task connect worker failed: %w", err)
// 					continue
// 				}
// 				isSend := wapi.AutoTaskLimit(lcli.ReqContext(cctx))
// 				if !isSend.IsSend{
// 					log.Error("auto task AutoTaskLimit failed")
// 					workerApiCloser()
// 					continue
// 				}
// 				workerApiCloser()

// 				lapi, lcloser, err := lcli.GetFullNodeAPI(cctx)
// 				if err != nil {
// 					log.Errorf("auto task connect lotus failed: %w", err)
// 					continue
// 				}
// 				workerBalance, err := lapi.WalletBalance(lcli.ReqContext(cctx), mInfo.Worker)
// 				if err != nil {
// 					log.Errorf("auto task connect worker failed: %w", err)
// 					lcloser()
// 					continue
// 				}
// 				lcloser()
// 				if types.BigCmp(workerBalance, autoTaskBalance) < 0 {
// 					log.Infof("workerBalance less than  autoTaskBalance, %s, %s", workerBalance.String(), autoTaskBalance.String())
// 					continue
// 				}
// 				mapi, minerApiCloser, err := lcli.GetStorageMinerAPI(cctx)
// 				if err != nil {
// 					log.Errorf("auto task connect miner failed: %w", err)
// 					continue
// 				}
// 				isSched, err := mapi.TrySched(lcli.ReqContext(cctx), isSend.Group, mInfo.SectorSize.ShortString())
// 				if !isSched || err != nil {
// 					log.Errorf("auto task TrySched failed: %w", err)
// 					minerApiCloser()
// 					continue
// 				}
// 				err = mapi.PledgeSector(lcli.ReqContext(cctx), isSend.Group)
// 				if err != nil {
// 					log.Errorf("auto task pledge sector failed: %w", err)
// 				}
// 				minerApiCloser()
// 				log.Info("auto task pledge sector success")
// 			case <-sigs:
// 				os.Exit(0)
// 			}

// 		}
// 		return nil
// 	},
// }

var runCmd = &cli.Command{
	Name:  "run",
	Usage: "Start lotus worker",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "listen",
			Usage: "host address and port the worker api will listen on",
			Value: "0.0.0.0:3456",
		},
		&cli.StringFlag{
			Name:   "address",
			Hidden: true,
		},
		&cli.BoolFlag{
			Name:  "no-local-storage",
			Usage: "don't use storageminer repo for sector storage",
		},
		&cli.BoolFlag{
			Name:  "no-swap",
			Usage: "don't use swap",
			Value: false,
		},
		&cli.BoolFlag{
			Name:  "addpiece",
			Usage: "enable addpiece",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "precommit1",
			Usage: "enable precommit1 (32G sectors: 1 core, 128GiB Memory)",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "unseal",
			Usage: "enable unsealing (32G sectors: 1 core, 128GiB Memory)",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "precommit2",
			Usage: "enable precommit2 (32G sectors: all cores, 96GiB Memory)",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "commit",
			Usage: "enable commit (32G sectors: all cores or GPUs, 128GiB Memory + 64GiB swap)",
			Value: true,
		},
		&cli.IntFlag{
			Name:  "parallel-fetch-limit",
			Usage: "maximum fetch operations to run in parallel",
			Value: 5,
		},
		&cli.StringFlag{
			Name:  "timeout",
			Usage: "used when 'listen' is unspecified. must be a valid duration recognized by golang's time.ParseDuration function",
			Value: "30m",
		},
		&cli.Int64Flag{
			Name:  "addpiecemax",
			Usage: "Allow the maximum number of simultaneous tasks for addpiece, default value: 0",
			Value: 0,
		},
		&cli.Int64Flag{
			Name:  "precommit1max",
			Usage: "Allow the maximum number of simultaneous tasks for precommit1, default value: 0",
			Value: 0,
		},
		&cli.Int64Flag{
			Name:  "precommit2max",
			Usage: "Allow the maximum number of simultaneous tasks for precommit2, default value: 0",
			Value: 0,
		},
		&cli.Int64Flag{
			Name:  "commitmax",
			Usage: "Allow the maximum number of simultaneous tasks for commit2, default value: 0",
			Value: 0,
		},
		&cli.StringFlag{
			Name:  "group",
			Usage: "Worker grouping function, default value: all",
			Value: "all",
		},
	},
	Before: func(cctx *cli.Context) error {
		if cctx.IsSet("address") {
			log.Warnf("The '--address' flag is deprecated, it has been replaced by '--listen'")
			if err := cctx.Set("listen", cctx.String("address")); err != nil {
				return err
			}
		}

		return nil
	},
	Action: func(cctx *cli.Context) error {
		log.Info("Starting lotus worker")

		if !cctx.Bool("enable-gpu-proving") {
			if err := os.Setenv("BELLMAN_NO_GPU", "true"); err != nil {
				return xerrors.Errorf("could not set no-gpu env: %+v", err)
			}
		}

		// Connect to storage-miner
		ctx := lcli.ReqContext(cctx)

		var nodeApi api.StorageMiner
		var closer func()
		var err error
		for {
			nodeApi, closer, err = lcli.GetStorageMinerAPI(cctx, lcli.StorageMinerUseHttp)
			if err == nil {
				_, err = nodeApi.Version(ctx)
				if err == nil {
					break
				}
			}
			fmt.Printf("\r\x1b[0KConnecting to miner API... (%s)", err)
			time.Sleep(time.Second)
			continue
		}

		defer closer()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Register all metric views
		if err := view.Register(
			metrics.DefaultViews...,
		); err != nil {
			log.Fatalf("Cannot register the view: %v", err)
		}

		v, err := nodeApi.Version(ctx)
		if err != nil {
			return err
		}
		if v.APIVersion != build.MinerAPIVersion {
			return xerrors.Errorf("lotus-miner API version doesn't match: expected: %s", api.Version{APIVersion: build.MinerAPIVersion})
		}
		log.Infof("Remote version %s", v)

		// Check params

		act, err := nodeApi.ActorAddress(ctx)
		if err != nil {
			return err
		}
		ssize, err := nodeApi.ActorSectorSize(ctx, act)
		if err != nil {
			return err
		}

		if cctx.Bool("commit") {
			if err := paramfetch.GetParams(ctx, build.ParametersJSON(), uint64(ssize)); err != nil {
				return xerrors.Errorf("get params: %w", err)
			}
		}

		var taskTypes []sealtasks.TaskType

		taskTypes = append(taskTypes, sealtasks.TTFetch, sealtasks.TTCommit1, sealtasks.TTFinalize)

		if cctx.Bool("addpiece") {
			taskTypes = append(taskTypes, sealtasks.TTAddPiece)
		}
		if cctx.Bool("precommit1") {
			taskTypes = append(taskTypes, sealtasks.TTPreCommit1)
		}
		if cctx.Bool("unseal") {
			taskTypes = append(taskTypes, sealtasks.TTUnseal)
		}
		if cctx.Bool("precommit2") {
			taskTypes = append(taskTypes, sealtasks.TTPreCommit2)
		}
		if cctx.Bool("commit") {
			taskTypes = append(taskTypes, sealtasks.TTCommit2)
		}

		if len(taskTypes) == 0 {
			return xerrors.Errorf("no task types specified")
		}

		// Open repo

		repoPath := cctx.String(FlagWorkerRepo)
		r, err := repo.NewFS(repoPath)
		if err != nil {
			return err
		}

		ok, err := r.Exists()
		if err != nil {
			return err
		}
		if !ok {
			if err := r.Init(repo.Worker); err != nil {
				return err
			}

			lr, err := r.Lock(repo.Worker)
			if err != nil {
				return err
			}

			var localPaths []stores.LocalPath

			if !cctx.Bool("no-local-storage") {
				b, err := json.MarshalIndent(&stores.LocalStorageMeta{
					ID:       stores.ID(uuid.New().String()),
					Weight:   10,
					CanSeal:  true,
					CanStore: false,
				}, "", "  ")
				if err != nil {
					return xerrors.Errorf("marshaling storage config: %w", err)
				}

				if err := ioutil.WriteFile(filepath.Join(lr.Path(), "sectorstore.json"), b, 0644); err != nil {
					return xerrors.Errorf("persisting storage metadata (%s): %w", filepath.Join(lr.Path(), "sectorstore.json"), err)
				}

				localPaths = append(localPaths, stores.LocalPath{
					Path: lr.Path(),
				})
			}

			if err := lr.SetStorage(func(sc *stores.StorageConfig) {
				sc.StoragePaths = append(sc.StoragePaths, localPaths...)
			}); err != nil {
				return xerrors.Errorf("set storage config: %w", err)
			}

			{
				// init datastore for r.Exists
				_, err := lr.Datastore(context.Background(), "/metadata")
				if err != nil {
					return err
				}
			}
			if err := lr.Close(); err != nil {
				return xerrors.Errorf("close repo: %w", err)
			}
		}

		lr, err := r.Lock(repo.Worker)
		if err != nil {
			return err
		}
		defer func() {
			if err := lr.Close(); err != nil {
				log.Error("closing repo", err)
			}
		}()
		ds, err := lr.Datastore(context.Background(), "/metadata")
		if err != nil {
			return err
		}

		log.Info("Opening local storage; connecting to master")
		const unspecifiedAddress = "0.0.0.0"
		address := cctx.String("listen")
		addressSlice := strings.Split(address, ":")
		if ip := net.ParseIP(addressSlice[0]); ip != nil {
			if ip.String() == unspecifiedAddress {
				timeout, err := time.ParseDuration(cctx.String("timeout"))
				if err != nil {
					return err
				}
				rip, err := extractRoutableIP(timeout)
				if err != nil {
					return err
				}
				address = rip + ":" + addressSlice[1]
			}
		}

		localStore, err := stores.NewLocal(ctx, lr, nodeApi, []string{"http://" + address + "/remote"})
		if err != nil {
			return err
		}

		// Setup remote sector store
		sminfo, err := lcli.GetAPIInfo(cctx, repo.StorageMiner)
		if err != nil {
			return xerrors.Errorf("could not get api info: %w", err)
		}

		remote := stores.NewRemote(localStore, nodeApi, sminfo.AuthHeader(), cctx.Int("parallel-fetch-limit"))

		fh := &stores.FetchHandler{Local: localStore}
		remoteHandler := func(w http.ResponseWriter, r *http.Request) {
			if !auth.HasPerm(r.Context(), nil, apistruct.PermAdmin) {
				w.WriteHeader(401)
				_ = json.NewEncoder(w).Encode(struct{ Error string }{"unauthorized: missing admin permission"})
				return
			}

			fh.ServeHTTP(w, r)
		}

		// Create / expose the worker

		wsts := statestore.New(namespace.Wrap(ds, modules.WorkerCallsPrefix))

		workerApi := &worker{
			LocalWorker: sectorstorage.NewLocalWorker(sectorstorage.WorkerConfig{
				TaskTypes:     taskTypes,
				AddPieceMax:   cctx.Int64("addpiecemax"),
				PreCommit1Max: cctx.Int64("precommit1max"),
				PreCommit2Max: cctx.Int64("precommit2max"),
				CommitMax:     cctx.Int64("commitmax"),
				Group:         cctx.String("group"),

				NoSwap: cctx.Bool("no-swap"),
			}, remote, localStore, nodeApi, nodeApi, wsts),
			localStore: localStore,
			ls:         lr,
		}

		mux := mux.NewRouter()

		log.Info("Setting up control endpoint at " + address)

		readerHandler, readerServerOpt := rpcenc.ReaderParamDecoder()
		rpcServer := jsonrpc.NewServer(readerServerOpt)
		rpcServer.Register("Filecoin", apistruct.PermissionedWorkerAPI(metrics.MetricedWorkerAPI(workerApi)))

		mux.Handle("/rpc/v0", rpcServer)
		mux.Handle("/rpc/streams/v0/push/{uuid}", readerHandler)
		mux.PathPrefix("/remote").HandlerFunc(remoteHandler)
		mux.PathPrefix("/").Handler(http.DefaultServeMux) // pprof

		ah := &auth.Handler{
			Verify: nodeApi.AuthVerify,
			Next:   mux.ServeHTTP,
		}

		srv := &http.Server{
			Handler: ah,
			BaseContext: func(listener net.Listener) context.Context {
				ctx, _ := tag.New(context.Background(), tag.Upsert(metrics.APIInterface, "lotus-worker"))
				return ctx
			},
		}

		go func() {
			<-ctx.Done()
			log.Warn("Shutting down...")
			if err := srv.Shutdown(context.TODO()); err != nil {
				log.Errorf("shutting down RPC server failed: %s", err)
			}
			log.Warn("Graceful shutdown successful")
		}()

		nl, err := net.Listen("tcp", address)
		if err != nil {
			return err
		}

		{
			a, err := net.ResolveTCPAddr("tcp", address)
			if err != nil {
				return xerrors.Errorf("parsing address: %w", err)
			}

			ma, err := manet.FromNetAddr(a)
			if err != nil {
				return xerrors.Errorf("creating api multiaddress: %w", err)
			}

			if err := lr.SetAPIEndpoint(ma); err != nil {
				return xerrors.Errorf("setting api endpoint: %w", err)
			}

			ainfo, err := lcli.GetAPIInfo(cctx, repo.StorageMiner)
			if err != nil {
				return xerrors.Errorf("could not get miner API info: %w", err)
			}

			// TODO: ideally this would be a token with some permissions dropped
			if err := lr.SetAPIToken(ainfo.Token); err != nil {
				return xerrors.Errorf("setting api token: %w", err)
			}
		}

		minerSession, err := nodeApi.Session(ctx)
		if err != nil {
			return xerrors.Errorf("getting miner session: %w", err)
		}

		waitQuietCh := func() chan struct{} {
			out := make(chan struct{})
			go func() {
				workerApi.LocalWorker.WaitQuiet()
				close(out)
			}()
			return out
		}

		go func() {
			heartbeats := time.NewTicker(stores.HeartbeatInterval)
			defer heartbeats.Stop()

			var redeclareStorage bool
			var readyCh chan struct{}
			for {
				// If we're reconnecting, redeclare storage first
				if redeclareStorage {
					log.Info("Redeclaring local storage")

					if err := localStore.Redeclare(ctx); err != nil {
						log.Errorf("Redeclaring local storage failed: %+v", err)

						select {
						case <-ctx.Done():
							return // graceful shutdown
						case <-heartbeats.C:
						}
						continue
					}
				}

				// TODO: we could get rid of this, but that requires tracking resources for restarted tasks correctly
				if readyCh == nil {
					log.Info("Making sure no local tasks are running")
					readyCh = waitQuietCh()
				}

				for {
					curSession, err := nodeApi.Session(ctx)
					if err != nil {
						log.Errorf("heartbeat: checking remote session failed: %+v", err)
					} else {
						if curSession != minerSession {
							minerSession = curSession
							break
						}
					}

					select {
					case <-readyCh:
						if err := nodeApi.WorkerConnect(ctx, "http://"+address+"/rpc/v0"); err != nil {
							log.Errorf("Registering worker failed: %+v", err)
							cancel()
							return
						}

						log.Info("Worker registered successfully, waiting for tasks")

						readyCh = nil
					case <-heartbeats.C:
					case <-ctx.Done():
						return // graceful shutdown
					}
				}

				log.Errorf("LOTUS-MINER CONNECTION LOST")

				redeclareStorage = true
			}
		}()

		return srv.Serve(nl)
	},
}

func extractRoutableIP(timeout time.Duration) (string, error) {
	minerMultiAddrKey := "MINER_API_INFO"
	deprecatedMinerMultiAddrKey := "STORAGE_API_INFO"
	env, ok := os.LookupEnv(minerMultiAddrKey)
	if !ok {
		// TODO remove after deprecation period
		_, ok = os.LookupEnv(deprecatedMinerMultiAddrKey)
		if ok {
			log.Warnf("Using a deprecated env(%s) value, please use env(%s) instead.", deprecatedMinerMultiAddrKey, minerMultiAddrKey)
		}
		return "", xerrors.New("MINER_API_INFO environment variable required to extract IP")
	}
	minerAddr := strings.Split(env, "/")
	conn, err := net.DialTimeout("tcp", minerAddr[2]+":"+minerAddr[4], timeout)
	if err != nil {
		return "", err
	}
	defer conn.Close() //nolint:errcheck

	localAddr := conn.LocalAddr().(*net.TCPAddr)

	return strings.Split(localAddr.IP.String(), ":")[0], nil
}
