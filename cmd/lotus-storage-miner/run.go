package main

import (
	"context"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	mux "github.com/gorilla/mux"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/urfave/cli/v2"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"golang.org/x/xerrors"

	scServer "github.com/moran666666/sector-counter/server"

	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/go-jsonrpc/auth"

	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/api/apistruct"
	"github.com/filecoin-project/lotus/build"
	lcli "github.com/filecoin-project/lotus/cli"
	"github.com/filecoin-project/lotus/lib/ulimit"
	"github.com/filecoin-project/lotus/metrics"
	"github.com/filecoin-project/lotus/node"
	"github.com/filecoin-project/lotus/node/impl"
	"github.com/filecoin-project/lotus/node/modules/dtypes"
	"github.com/filecoin-project/lotus/node/repo"
)

var runCmd = &cli.Command{
	Name:  "run",
	Usage: "Start a lotus miner process",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "miner-api",
			Usage: "2345",
		},
		&cli.BoolFlag{
			Name:  "enable-gpu-proving",
			Usage: "enable use of GPU for mining operations",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "nosync",
			Usage: "don't check full-node sync status",
		},
		&cli.BoolFlag{
			Name:  "manage-fdlimit",
			Usage: "manage open file limit",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "wdpost",
			Usage: "enable windowPoSt",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "wnpost",
			Usage: "enable winningPoSt",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "p2p",
			Usage: "enable P2P",
			Value: true,
		},
		&cli.StringFlag{
			Name:  "sctype",
			Usage: "sector counter type(alloce,get)",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "sclisten",
			Usage: "host address and port the sector counter will listen on",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "workername",
			Usage: "worker name will display on jobs list",
			Value: "",
		},
	},
	Action: func(cctx *cli.Context) error {
		if !cctx.Bool("enable-gpu-proving") {
			err := os.Setenv("BELLMAN_NO_GPU", "true")
			if err != nil {
				return err
			}
		}

		if cctx.Bool("wdpost") {
			os.Setenv("LOTUS_WDPOST", "true")
		} else {
			os.Unsetenv("LOTUS_WDPOST")
		}

		if cctx.Bool("wnpost") {
			os.Setenv("LOTUS_WNPOST", "true")
		} else {
			os.Unsetenv("LOTUS_WNPOST")
		}

		scType := cctx.String("sctype")
		if scType == "alloce" || scType == "get" {
			os.Setenv("SC_TYPE", scType)

			scListen := cctx.String("sclisten")
			if scListen == "" {
				log.Errorf("sclisten must be set")
				return nil
			}
			os.Setenv("SC_LISTEN", scListen)

			if scType == "alloce" {
				scFilePath := filepath.Join(cctx.String(FlagMinerRepo), "sectorid")
				go scServer.Run(scFilePath)
			}
		} else {
			os.Unsetenv("SC_TYPE")
		}

		if cctx.String("workername") != "" {
			os.Setenv("WORKER_NAME", cctx.String("workername"))
		}


		nodeApi, ncloser, err := lcli.GetFullNodeAPI(cctx)
		if err != nil {
			return xerrors.Errorf("getting full node api: %w", err)
		}
		defer ncloser()
		ctx := lcli.DaemonContext(cctx)

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

		if cctx.Bool("manage-fdlimit") {
			if _, _, err := ulimit.ManageFdLimit(); err != nil {
				log.Errorf("setting file descriptor limit: %s", err)
			}
		}

		if v.APIVersion != build.FullAPIVersion {
			return xerrors.Errorf("lotus-daemon API version doesn't match: expected: %s", api.Version{APIVersion: build.FullAPIVersion})
		}

		log.Info("Checking full node sync status")

		if !cctx.Bool("nosync") {
			if err := lcli.SyncWait(ctx, nodeApi, false); err != nil {
				return xerrors.Errorf("sync wait: %w", err)
			}
		}

		minerRepoPath := cctx.String(FlagMinerRepo)
		r, err := repo.NewFS(minerRepoPath)
		if err != nil {
			return err
		}

		ok, err := r.Exists()
		if err != nil {
			return err
		}
		if !ok {
			return xerrors.Errorf("repo at '%s' is not initialized, run 'lotus-miner init' to set it up", minerRepoPath)
		}

		shutdownChan := make(chan struct{})

		var minerapi api.StorageMiner
		stop, err := node.New(ctx,
			node.StorageMiner(&minerapi),
			node.Override(new(dtypes.ShutdownChan), shutdownChan),
			node.Online(),
			node.Repo(r),

			node.ApplyIf(func(s *node.Settings) bool { return cctx.IsSet("miner-api") },
				node.Override(new(dtypes.APIEndpoint), func() (dtypes.APIEndpoint, error) {
					return multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/" + cctx.String("miner-api"))
				})),
			node.Override(new(api.FullNode), nodeApi),
		)
		if err != nil {
			return xerrors.Errorf("creating node: %w", err)
		}

		endpoint, err := r.APIEndpoint()
		if err != nil {
			return xerrors.Errorf("getting API endpoint: %w", err)
		}
		if cctx.Bool("p2p") {
			// Bootstrap with full node
			remoteAddrs, err := nodeApi.NetAddrsListen(ctx)
			if err != nil {
				return xerrors.Errorf("getting full node libp2p address: %w", err)
			}

			if err := minerapi.NetConnect(ctx, remoteAddrs); err != nil {
				return xerrors.Errorf("connecting to full node (libp2p): %w", err)
			}
		}else {
			log.Warn("This miner will be disable p2p.")
		}
		
		log.Infof("Remote version %s", v)

		lst, err := manet.Listen(endpoint)
		if err != nil {
			return xerrors.Errorf("could not listen: %w", err)
		}

		mux := mux.NewRouter()

		rpcServer := jsonrpc.NewServer()
		rpcServer.Register("Filecoin", apistruct.PermissionedStorMinerAPI(metrics.MetricedStorMinerAPI(minerapi)))

		mux.Handle("/rpc/v0", rpcServer)
		mux.PathPrefix("/remote").HandlerFunc(minerapi.(*impl.StorageMinerAPI).ServeRemote)
		mux.PathPrefix("/").Handler(http.DefaultServeMux) // pprof

		ah := &auth.Handler{
			Verify: minerapi.AuthVerify,
			Next:   mux.ServeHTTP,
		}

		srv := &http.Server{
			Handler: ah,
			BaseContext: func(listener net.Listener) context.Context {
				ctx, _ := tag.New(context.Background(), tag.Upsert(metrics.APIInterface, "lotus-miner"))
				return ctx
			},
		}

		sigChan := make(chan os.Signal, 2)
		go func() {
			select {
			case sig := <-sigChan:
				log.Warnw("received shutdown", "signal", sig)
			case <-shutdownChan:
				log.Warn("received shutdown")
			}

			log.Warn("Shutting down...")
			if err := stop(context.TODO()); err != nil {
				log.Errorf("graceful shutting down failed: %s", err)
			}
			if err := srv.Shutdown(context.TODO()); err != nil {
				log.Errorf("shutting down RPC server failed: %s", err)
			}
			log.Warn("Graceful shutdown successful")
		}()
		signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

		return srv.Serve(manet.NetListener(lst))
	},
}
