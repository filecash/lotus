package genesis

import (
	"encoding/hex"

	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

const genesisMultihashString = "12204fed2188046df271f335881ddbcc363f091c943d464a5b0199b64d7e1ba65094"
const genesisBlockHex = "4461746574696d6573323031392d31302d32302031363a35333a30354e6574776f726b6846696c6563617368546f6b656e6846696c65636173686c546f6b656e416d6f756e7473a36b546f74616c537570706c796d322c3030302c3030302c303030664d696e6572736d312c3730302c3030302c3030306c4d657373616765784854686973206973207468652047656e6573697320426c6f636b206f66207468652046696c656361736820446563656e7472616c697a65642053746f72616765204e6574776f726b2e"

var cidBuilder = cid.V1Builder{Codec: cid.DagCBOR, MhType: multihash.SHA2_256}

func expectedCid() cid.Cid {
	mh, err := multihash.FromHexString(genesisMultihashString)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cidBuilder.Codec, mh)
}

func getGenesisBlock() (blocks.Block, error) {
	genesisBlockData, err := hex.DecodeString(genesisBlockHex)
	if err != nil {
		return nil, err
	}

	genesisCid, err := cidBuilder.Sum(genesisBlockData)
	if err != nil {
		return nil, err
	}

	block, err := blocks.NewBlockWithCid(genesisBlockData, genesisCid)
	if err != nil {
		return nil, err
	}

	return block, nil
}
