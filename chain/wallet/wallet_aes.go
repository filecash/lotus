package wallet

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"

	"golang.org/x/xerrors"
)

/*
 #include <stdio.h>
 #include <termios.h>
 struct termios disable_echo() {
 	struct termios of, nf;
 	tcgetattr(fileno(stdin), &of);
 	nf = of;
 	nf.c_lflag &= ~ECHO;
 	nf.c_lflag |= ECHONL;
 	if (tcsetattr(fileno(stdin), TCSANOW, &nf) != 0) {
 		perror("tcsetattr");
   	}
 	return of;
 }
 void restore_echo(struct termios f) {
 	if (tcsetattr(fileno(stdin), TCSANOW, &f) != 0) {
 		perror("tcsetattr");
 	}
 }
*/
import "C"

var WalletPasswd string = ""
var passwdPath string = ""

const checkMsg string = "check passwd is success"

func AESEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, xerrors.Errorf("passwd must be 16 character")
	}

	ciphertext := make([]byte, aes.BlockSize+len(plaintext))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}

	stream := cipher.NewCFBEncrypter(block, iv)
	stream.XORKeyStream(ciphertext[aes.BlockSize:], plaintext)

	return ciphertext, nil
}

func AESDecrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, xerrors.Errorf("passwd must be 16 character")
	} else if len(ciphertext) < aes.BlockSize {
		return nil, xerrors.Errorf("passwd must be 16 character")
	}

	iv := ciphertext[:aes.BlockSize]
	ciphertext = ciphertext[aes.BlockSize:]
	stream := cipher.NewCFBDecrypter(block, iv)
	stream.XORKeyStream(ciphertext, ciphertext)

	return ciphertext, nil
}

func SetupPasswd(key []byte, path string) error {
	_, err := os.Stat(path)
	if err == nil {
		return xerrors.Errorf("checking file before Setup passwd '%s': file already exists", path)
	} else if !os.IsNotExist(err) {
		return xerrors.Errorf("checking file before Setup passwd '%s': %w", path, err)
	}

	msg, err := AESEncrypt(key, []byte(checkMsg))
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(path, msg, 0600)
	if err != nil {
		return xerrors.Errorf("writing file '%s': %w", path, err)
	}

	WalletPasswd = string(key)
	passwdPath = path

	return nil
}

func ResetPasswd(passwd []byte) error {
	err := os.Remove(passwdPath)
	if err != nil {
		return err
	}

	err = SetupPasswd(passwd, passwdPath)
	if err != nil {
		return err
	}

	return nil
}

func ClearPasswd() error {
	err := os.Remove(passwdPath)
        if err != nil {
                return err
        }
	WalletPasswd = ""
	passwdPath = ""
	return nil
}

func CheckPasswd(key []byte) error {
	fstat, err := os.Stat(passwdPath)
	if os.IsNotExist(err) {
		return xerrors.Errorf("opening file '%s': file info not found", passwdPath)
	} else if err != nil {
		return xerrors.Errorf("opening file '%s': %w", passwdPath, err)
	}

	if fstat.Mode()&0077 != 0 {
		return xerrors.Errorf("permissions of key: '%s' are too relaxed, required: 0600, got: %#o", passwdPath, fstat.Mode())
	}

	file, err := os.Open(passwdPath)
	if err != nil {
		return xerrors.Errorf("opening file '%s': %w", passwdPath, err)
	}
	defer file.Close()

	data, err := ioutil.ReadAll(file)
	if err != nil {
		return xerrors.Errorf("reading file '%s': %w", passwdPath, err)
	}

	text, err := AESDecrypt(key, data)
	if err != nil {
		return err
	}

	str := string(text)
	if checkMsg != str {
		return xerrors.Errorf("check passwd is failed")
	}

	return nil
}

func GetSetupState(path string) bool {
	fstat, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	} else if err != nil {
		return false
	}

	if fstat.Mode()&0077 != 0 {
		return false
	}

	passwdPath = path

	return true
}

func IsSetup() bool {
	if passwdPath == "" {
		return false
	}
	return true
}

func Prompt(msg string) string {
	fmt.Printf("%s", msg)
	oldFlags := C.disable_echo()
	passwd, err := bufio.NewReader(os.Stdin).ReadString('\n')
	C.restore_echo(oldFlags)
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(passwd)
}

func MakeByte(pk []byte, encrypt bool) ([]byte, error) {
	if IsSetup() {
		fmt.Println("Passwd is setup")
		if WalletPasswd == "" {
			fmt.Println("Wallet is lock")
			if pk[0] != 0xff && pk[1] != 0xff && pk[2] != 0xff && pk[3] != 0xff && !encrypt {
				fmt.Println("Private Key is not encrypt")
				return pk, nil
			}
			return nil, xerrors.Errorf("Wallet is lock")
		} else {
			fmt.Println("Wallet is unlock")
			if pk[0] == 0xff && pk[1] == 0xff && pk[2] == 0xff && pk[3] == 0xff {
				fmt.Println("Decrypt Private Key")
				msg := make([]byte, len(pk)-4)
				for i := range pk {
					if i >= 4 {
						msg[i-4] = pk[i]
					}
				}
				return AESDecrypt([]byte(WalletPasswd), msg)
			} else if encrypt {
				fmt.Println("Encrypt Private Key")
				msg, err := AESEncrypt([]byte(WalletPasswd), pk)
				if err != nil {
					return nil, err
				}
				text := make([]byte, len(msg)+4)
				text[0] = 0xff
				text[1] = 0xff
				text[2] = 0xff
				text[3] = 0xff
				for i := range msg {
					text[4+i] = msg[i]
				}
				return text, nil
			}
			fmt.Println("Keep Private Key")
		}
	} else {
		fmt.Println("Passwd is not setup")
	}
	return pk, nil
}
