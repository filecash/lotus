package main

import (
	"bufio"
	"fmt"
	"io"
	. "log"
	"os"
	"os/exec"
	"strings"
)

func MakeSectorTXT(ids, dir, path string) string {
	scrips := `
rm -rf /sectors.txt
ls -l "$2"   | awk '{print $9}' | awk -F '-' '{print $3}' |uniq >id.txt
OLD_IFS="$IFS"
IFS=","
array=($1)
IFS="$OLD_IFS"
for sectorid in ` + "`cat id.txt`" + `; do
  for var in ${array[@]}; do
    if [ $sectorid -eq $var ]
    then
        lotus-miner sectors status $sectorid | grep CIDcommR -B3 | grep -v "Status\|CIDcommD\|--" >> /sectors.txt
    fi
  done
done
`

	fmt.Print(WriteFile("scritps.sh", []byte(scrips), 0666), "#<")
	command := `chmod +x scritps.sh && ./scritps.sh   ` + ids + `   ` + dir + `/sealed/`
	//command := `cp cmd/sector-checker/scritps.sh . && chmod +x scritps.sh && ./scritps.sh   ` + ids + `   ` + dir + `/sealed/`
	fmt.Print(command)
	cmd := exec.Command("/bin/bash", "-c", command)

	bytes, err := cmd.Output()
	if err != nil {
		panic(err)
	}
	resp := string(bytes)
	Println(resp)

	return resp
}
func MakeSectorTXTnotid(dir, path string) string {

	command := `ls -l ` + dir + `/sealed/  | awk '{print $9}' | awk -F '-' '{print $3}' |xargs -i lotus-miner sectors status {} | grep CIDcommR -B3 | grep -v "Status|CIDcommD|--"  | tee ` + path
	fmt.Print(command)
	cmd := exec.Command("/bin/bash", "-c", command)

	bytes, err := cmd.Output()
	if err != nil {
		panic(err)
	}
	resp := string(bytes)
	Println(resp)

	return resp
}

func FixSectorTXT(ids, dir, path string) {
	if len(ids) > 0 {
		MakeSectorTXT(ids, dir, path)
	} else {
		MakeSectorTXTnotid(dir, path)
	}
	fi, err := os.Open(path)
	if err != nil {
		fmt.Printf("Error: %s\n", err)
		panic(err)
	}
	defer fi.Close()
	maps := []string{}
	li := []string{}
	br := bufio.NewReader(fi)
	//os.Remove(path)
	for {
		a, _, c := br.ReadLine()
		if c == io.EOF {
			break
		}
		content := string(a)
		if strings.HasPrefix(content, "SectorID:") {

			ins := strings.Index(content, "SectorID:")
			maps = append(maps, strings.ReplaceAll(content[ins+10:], " ", "")+"\n")
		}
		if strings.HasPrefix(content, "CIDcommR:") {
			ins := strings.Index(content, "CIDcommR:")
			maps = append(maps, strings.ReplaceAll(content[ins+10:], " ", "")+"\n")

			li = append(li, strings.Join(maps, ""))
			maps = []string{}
		}

	}
	WriteFile(path, []byte(strings.Join(li, "")), 0666)
}

func checkFileIsExist(filename string) bool {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return false
	}
	return true
}
func Writetxt(content, filepath string) {
	var str = content
	var filename = filepath
	var f *os.File
	var err1 error
	if checkFileIsExist(filename) {
		f, err1 = os.OpenFile(filename, os.O_APPEND, 0666) //open
		fmt.Println("file exist")
	} else {
		f, err1 = os.Create(filename)
		fmt.Println("file isn't exist")
	}
	defer f.Close()
	if err1 != nil {
		panic(err1)
	}
	w := bufio.NewWriter(f) //create writer
	n, _ := w.WriteString(str)
	fmt.Printf("write %d byte", n)
	w.Flush()
}

func WriteFile(filename string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if err == nil && n < len(data) {
		err = io.ErrShortWrite
	}
	if err1 := f.Close(); err == nil {
		err = err1
	}
	return err
}
