package build

import (
	"sort"

	"github.com/filecoin-project/lotus/node/modules/dtypes"
)

type DrandEnum int

func DrandConfigSchedule() dtypes.DrandSchedule {
	out := dtypes.DrandSchedule{}
	for start, config := range DrandSchedule {
		out = append(out, dtypes.DrandPoint{Start: start, Config: DrandConfigs[config]})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Start < out[j].Start
	})

	return out
}

const (
	DrandMainnet DrandEnum = iota + 1
	DrandTestnet
	DrandDevnet
	DrandLocalnet
	DrandIncentinet
)

var DrandConfigs = map[DrandEnum]dtypes.DrandConfig{
	DrandMainnet: {
		Servers: []string{
			"https://api.drand.sh",
			"https://api2.drand.sh",
			"https://api3.drand.sh",
		},
		Relays: []string{
			"/dnsaddr/api.drand.sh/",
			"/dnsaddr/api2.drand.sh/",
			"/dnsaddr/api3.drand.sh/",
		},
		ChainInfoJSON: `{"public_key":"868f005eb8e6e4ca0a47c8a77ceaa5309a47978a7c71bc5cce96366b5d7a569937c529eeda66c7293784a9402801af31","period":30,"genesis_time":1595431050,"hash":"8990e7a9aaed2ffed73dbd7092123d6f289930540d7651336225dc172e51b2ce","groupHash":"176f93498eac9ca337150b46d21dd58673ea4e3581185f869672e59fa4cb390a"}`,
	},
	DrandTestnet: {
		Servers: []string{
			"https://api1.drand.top",
			"https://api2.drand.top",
			"https://api3.drand.top",
		},
		Relays:        []string{},
		ChainInfoJSON: `{"public_key":"802b72507592ecf040e7c95a9d96d9e950b0dd426be36c62e2cdf3d4cb2a307acb5efa7c2fe222087e450f6e9ccc5257","period":30,"genesis_time":1602883530,"hash":"977236ee92f8edff9e28ab68bd0c5849be5226800a344e490267dd9950b273f3","groupHash":"18d8227dad40642b74463a492b37d43e1a8d3149710383d8cd5dadd2b241b2c4"}`,
	},
	DrandDevnet: {
		Servers: []string{
			"https://dev1.drand.sh",
			"https://dev2.drand.sh",
		},
		Relays: []string{
			"/dnsaddr/dev1.drand.sh/",
			"/dnsaddr/dev2.drand.sh/",
		},
		ChainInfoJSON: `{"public_key":"8cda589f88914aa728fd183f383980b35789ce81b274e5daee1f338b77d02566ef4d3fb0098af1f844f10f9c803c1827","period":25,"genesis_time":1595348225,"hash":"e73b7dc3c4f6a236378220c0dd6aa110eb16eed26c11259606e07ee122838d4f","groupHash":"567d4785122a5a3e75a9bc9911d7ea807dd85ff76b78dc4ff06b075712898607"}`,
	},
	DrandIncentinet: {
		Servers: []string{
			"https://api1.drand.top",
			"https://api2.drand.top",
			"https://api3.drand.top",
		},
		Relays:        []string{},
		ChainInfoJSON: `{"public_key":"802b72507592ecf040e7c95a9d96d9e950b0dd426be36c62e2cdf3d4cb2a307acb5efa7c2fe222087e450f6e9ccc5257","period":30,"genesis_time":1602883530,"hash":"977236ee92f8edff9e28ab68bd0c5849be5226800a344e490267dd9950b273f3","groupHash":"18d8227dad40642b74463a492b37d43e1a8d3149710383d8cd5dadd2b241b2c4"}`,
	},
}
