package full

import (
	"context"

	"go.uber.org/fx"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/crypto"

	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/chain/stmgr"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/chain/wallet"
	"github.com/filecoin-project/lotus/lib/sigs"
)

type WalletAPI struct {
	fx.In

	StateManagerAPI stmgr.StateManagerAPI
	Default         wallet.Default
	api.WalletAPI
}

func (a *WalletAPI) WalletBalance(ctx context.Context, addr address.Address) (types.BigInt, error) {
	act, err := a.StateManagerAPI.LoadActorTsk(ctx, addr, types.EmptyTSK)
	if xerrors.Is(err, types.ErrActorNotFound) {
		return big.Zero(), nil
	} else if err != nil {
		return big.Zero(), err
	}
	return act.Balance, nil
}

func (a *WalletAPI) WalletSign(ctx context.Context, k address.Address, msg []byte) (*crypto.Signature, error) {
	keyAddr, err := a.StateManagerAPI.ResolveToKeyAddress(ctx, k, nil)
	if err != nil {
		return nil, xerrors.Errorf("failed to resolve ID address: %w", keyAddr)
	}
	return a.WalletAPI.WalletSign(ctx, keyAddr, msg, api.MsgMeta{
		Type: api.MTUnknown,
	})
}

func (a *WalletAPI) WalletSignMessage(ctx context.Context, k address.Address, msg *types.Message) (*types.SignedMessage, error) {
	keyAddr, err := a.StateManagerAPI.ResolveToKeyAddress(ctx, k, nil)
	if err != nil {
		return nil, xerrors.Errorf("failed to resolve ID address: %w", keyAddr)
	}

	mb, err := msg.ToStorageBlock()
	if err != nil {
		return nil, xerrors.Errorf("serializing message: %w", err)
	}

	sig, err := a.WalletAPI.WalletSign(ctx, keyAddr, mb.Cid().Bytes(), api.MsgMeta{
		Type:  api.MTChainMsg,
		Extra: mb.RawData(),
	})
	if err != nil {
		return nil, xerrors.Errorf("failed to sign message: %w", err)
	}

	return &types.SignedMessage{
		Message:   *msg,
		Signature: *sig,
	}, nil
}

func (a *WalletAPI) WalletVerify(ctx context.Context, k address.Address, msg []byte, sig *crypto.Signature) (bool, error) {
	return sigs.Verify(sig, k, msg) == nil, nil
}

func (a *WalletAPI) WalletDefaultAddress(ctx context.Context) (address.Address, error) {
	return a.Default.GetDefault()
}

func (a *WalletAPI) WalletSetDefault(ctx context.Context, addr address.Address) error {
	return a.Default.SetDefault(addr)
}

// func (a *WalletAPI) WalletExport(ctx context.Context, addr address.Address, passwd string) (*types.KeyInfo, error) {
// 	oldPasswd := wallet.WalletPasswd
// 	wallet.WalletPasswd = passwd
// 	defer func() {
// 		wallet.WalletPasswd = oldPasswd
// 	}()
// 	return a.Default.Export(addr)
// }

// func (a *WalletAPI) WalletImport(ctx context.Context, ki *types.KeyInfo) (address.Address, error) {
// 	return a.Default.Import(ki)
// }

// func (a *WalletAPI) WalletDelete(ctx context.Context, addr address.Address, passwd string) error {
// 	oldPasswd := wallet.WalletPasswd
// 	wallet.WalletPasswd = passwd
// 	defer func() {
// 		wallet.WalletPasswd = oldPasswd
// 	}()
// 	return a.Default.DeleteKey(addr)
// }

func (a *WalletAPI) WalletValidateAddress(ctx context.Context, str string) (address.Address, error) {
	return address.NewFromString(str)
}

/*
func (a *WalletAPI) WalletLock(ctx context.Context) error {
	if wallet.IsSetup() {
		if wallet.WalletPasswd != "" {
			wallet.WalletPasswd = ""
			return nil
		} else {
			return xerrors.Errorf("Wallet is locked")
		}
	}

	return xerrors.Errorf("Passwd is not setup")
}

func (a *WalletAPI) WalletUnlock(ctx context.Context, passwd string) error {
	if wallet.IsSetup() {
		if wallet.WalletPasswd == "" {
			err := wallet.CheckPasswd([]byte(passwd))
			if err != nil {
				return err
			}
			wallet.WalletPasswd = passwd
			return nil
		} else {
			return xerrors.Errorf("Wallet is unlocked")
		}
	}

	return xerrors.Errorf("Passwd is not setup")
}

// func (a *WalletAPI) WalletIsLock(ctx context.Context) (bool, error) {
// 	if wallet.IsSetup() {
// 		if wallet.WalletPasswd == "" {
// 			return true, nil
// 		}
// 		return false, nil
// 	}
// 	return false, xerrors.Errorf("Passwd is not setup")
// }

func (a *WalletAPI) WalletChangePasswd(ctx context.Context, newPasswd string) (bool, error) {
	if wallet.IsSetup() {
		if wallet.WalletPasswd != "" {
			addr_list, err := a.WalletList(ctx)

			if err != nil {
				return false, err
			}
			addr_all := make(map[address.Address]*types.KeyInfo)
			for _, v := range addr_list {
				addr_all[v], err = a.WalletExport(ctx, v, wallet.WalletPasswd)
				if err != nil {
					return false, err
				}
				err = a.DeleteKey2(v)
				if err != nil {
					return false, err
				}
			}

			setDefault := true
			defalutAddr, err := a.Default.GetDefault()
			if err != nil {
				setDefault = false
			}

			if len(newPasswd) != 16 {
				return false, xerrors.Errorf("passwd must 16 character")
			}

			err = wallet.ResetPasswd([]byte(newPasswd))
			if err != nil {
				return false, err
			}

			for k, v := range addr_all {
				addr, err := a.WalletImport(ctx, v)
				if err != nil {
					return false, nil
				} else if addr != k {
					return false, xerrors.Errorf("import error")
				}
			}

			if setDefault {
				err = a.Default.SetDefault(defalutAddr)
				if err != nil {
					return false, err
				}
			}

			a.ClearCache()

			return true, nil
		}
		return false, xerrors.Errorf("Wallet is locked")
	}
	return false, xerrors.Errorf("Passwd is not setup")
}

func (a *WalletAPI) WalletClearPasswd(ctx context.Context) (bool, error) {
	if wallet.IsSetup() {
		if wallet.WalletPasswd != "" {
			addr_list, err := a.WalletList(ctx)
			if err != nil {
				return false, err
			}
			addr_all := make(map[address.Address]*types.KeyInfo)
			for _, v := range addr_list {
				addr_all[v], err = a.WalletExport(ctx, v, wallet.WalletPasswd)
				if err != nil {
					return false, err
				}
				err = a.DeleteKey2(v)
				if err != nil {
					return false, err
				}
			}

			setDefault := true
			defalutAddr, err := a.Default.GetDefault()
			if err != nil {
				setDefault = false
			}

			err = wallet.ClearPasswd()
			if err != nil {
				return false, err
			}

			for k, v := range addr_all {
				addr, err := a.WalletImport(ctx, v)
				if err != nil {
					return false, nil
				} else if addr != k {
					return false, xerrors.Errorf("import error")
				}
			}

			if setDefault {
				err = a.Default.SetDefault(defalutAddr)
				if err != nil {
					return false, err
				}
			}

			a.ClearCache()

			return true, nil
		}
		return false, xerrors.Errorf("Wallet is locked")
	}
	return false, xerrors.Errorf("Passwd is not setup")
}
*/
