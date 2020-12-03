#export LOTUS_PATH=/mnt/lotus
#export LOTUS_MINER_PATH=/mnt/miner
rm -rf /sectors.txt
ls -l "$2"   | awk '{print $9}' | awk -F '-' '{print $3}' |uniq >id.txt
OLD_IFS="$IFS"
IFS=","
array=($1)
IFS="$OLD_IFS"
for sectorid in `cat id.txt`; do
  for var in ${array[@]}; do
    if [ $sectorid -eq $var ]
    then
        lotus-miner sectors status $sectorid | grep CIDcommR -B3 | grep -v "Status\|CIDcommD\|--" >> /sectors.txt
    fi
  done
done