# env
git clone https://github.com/filecash/lotus_builder
bash build.sh -a

# build checker
cd lotus
make lotus-checker

# Scan all sectors
./lotus-checker checking --miner-id=t01000 --sector-size=4GiB --storage-dir=/hdd/miner_store
# Scan 0,1,2 sector
./lotus-checker checking --miner-id=t01000 --sector-size=4GiB --sector-id-fliter=0,1,2 --storage-dir=/hdd/miner_store

