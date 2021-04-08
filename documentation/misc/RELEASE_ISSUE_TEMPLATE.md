> Release Issue Template

# Lotus X.Y.Z Release

We're happy to announce Lotus X.Y.Z...

## 🗺 What's left for release

<List of items with PRs and/or Issues to be considered for this release>

## 🚢 Estimated shipping date

<Date this release will ship on if everything goes to plan (week beginning...)>

## 🔦 Highlights

< top highlights for this release notes >

## Changelog

< changelog generated by bin/mkreleaselog >

## ✅ Release Checklist

First steps:

  - [ ] Fork a new branch (`release/vX.Y.Z`) from `master` and make any further release related changes to this branch. If any "non-trivial" changes get added to the release, uncheck all the checkboxes and return to this stage.
  - [ ] Prep the changelog
  - [ ] Bump the version in `version.go` in the `master` branch to `vX.(Y+1).0-dev`.
  - [ ] Follow the RC release process to cut the first RC.
    
Prepping an RC:

- [ ] version string in `build/version.go` has been updated (in the `release/vX.Y.Z` branch).
- [ ] tag commit with `vX.Y.Z-rcN`
- [ ] cut a pre-release on [github](https://github.com/filecoin-project/lotus/releases)

Testing an RC:

- [ ] **Stage 0 - Automated Testing**
  - Automated Testing
    - [ ] CI: Ensure that all tests are passing.
    - [ ] Testground tests

- [ ] **Stage 1 - Internal Testing**
  - Upgrade our testnet infra
    - [ ] 1 bootstrap node
    - [ ] 1 miner
    - [ ] Scratch nodes
    - [ ] Wait 24 hours
    - [ ] Remaining testnet infra
  -  Upgrade our mainnet infra
    - [ ] Subset of development full archival nodes
    - [ ]Subset of bootstrappers (1 per region)
    - Report on new block validation time
    - TODO: What other stats would we care about?
    - If anything has worsened significantly, investigate + fix
  - Confirm the following work (some combination of Testground / Calibnet / Mainnet / MinerX)
    - [ ] Seal a sector
    - [ ] make a deal
    - [ ] Submit a PoSt
    - [ ] (ideally) let a sector go faulty, and see it be recovered
    
- [ ] **Stage 2 - Community Dev Testing**
  - [ ] Inform MinerX / early testers
  - [ ] Ask close ecosystem partners to test their projects with the upgrade
    - [ ] Powergate
    - TODO: List of partners

- [ ] **Stage 3 - Community Prod Testing**
  - [ ] Documentation
    - [ ] Ensure that [CHANGELOG.md](https://github.com/filecoin-project/lotus/blob/master/CHANGELOG.md) is up to date
    - [ ] TODO: Other docs checks?
  - [ ] Invite the wider community through (link to the release issue):
    - [ ] Create a lotus disucssion, example [here](https://github.com/filecoin-project/lotus/discussions/5595)
    - [ ] Link the disucssion in #fil-lotus on Filecoin slack
    
- [ ] **Stage 4 - Release**
  - [ ] Final preparation
    - [ ] Verify that version string in [`version.go`](https://github.com/ipfs/go-ipfs/tree/master/version.go) has been updated.
    - [ ] Ensure that [CHANGELOG.md](https://github.com/filecoin-project/lotus/blob/master/CHANGELOG.md) is up to date
    - [ ] Ensure that [README.md](https://github.com/filecoin-project/lotus/blob/master/README.md) is up to date
    - [ ] Merge `release-vX.Y.Z` into the `releases` branch.
    - [ ] Tag this merge commit (on the `releases` branch) with `vX.Y.Z`.
    - [ ] Cut the release on Github.
    - [ ] Final announcements
        - [ ] Update network.filecoin.io
        - [ ] TODO: What / where else?

- [ ] **Post-Release**
  - [ ] Merge the `releases` branch back into `master`, ignoring the changes to `version.go` (keep the `-dev` version from master).
  - [ ] Create an issue using this release issue template for the _next_ release.

## ❤️ Contributors

< list generated by bin/mkreleaselog >

Would you like to contribute to Lotus and don't know how? Well, there are a few places you can get started:

- TODO

## ⁉️ Do you have questions?

TODO