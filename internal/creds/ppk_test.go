package creds

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Fixtures are real PuTTY keys (borrowed from the kayrus/putty test suite):
// an unencrypted PPK v3 RSA key and an encrypted PPK v2 ed25519 key whose
// passphrase is "testkey". They let the conversion be tested without puttygen.

const ppkRSAUnencrypted = `PuTTY-User-Key-File-3: ssh-rsa
Encryption: none
Comment: a@b
Public-Lines: 6
AAAAB3NzaC1yc2EAAAADAQABAAABAQDNsvsFOGphVzbJJAARnMs2E9p6jheXLTz7
dnZqNwZCYomnGurAPEuKmxD3GzdT+xP4BLFbAGDkeJHmjiNAPnbJf7G90u2zD28Y
J/c/krfKli50ZUOXG1a2DUhIvRM1GewOLhE7q5AOBHLQNFXvU9LR08t9H3u9xPJI
xNJjP6LqRGn+fP1xqlTbG3NTwCZMMXgXuAUhXGKaKbLUBN5SYmLvLTB6KzdHJQ6x
H9X+2Ul4hExje5L2X8miQqTxPloNtQNqpEtR2X7ecLyM9v3N1yDUK/NLwJ+PX8C8
KRbuBi5+xp+k62+btFXIk6CgGpsda/KleLmzTk5QJGLA9DfzrvAd
Private-Lines: 14
AAABAQCWR5StE7Jku1sDSJHkTDEKqSaNMxJ5GEvdS4bnwpuIFIWM2FV5bJOkB/Y1
EmUxrdXA9Wy9l2EyigPN9To7zWbrf6dTj66pizUW6NvyTjaIg4Ac+X6P/yEykDGn
Mru9p9qV4YIlngn4s7dN9W5zE0KKmbmpCD9XPXPlRiaO7AcSLujUHp7kPij2i9EL
vYRy0TS2g/HbQlBiaCS3+RI5K1UrwSP/MUFzmy319ZuI5XZUz7Z7OER4tgFi8qth
HqPkvBTnbi3ORIhRQQT+faEmKHwyDuXTXlITWj+1k3wY6sdr308OfRut6OcH417U
/YcZfBK6A3iZ9AJ/ih1Sqd0xCDkBAAAAgQD6IYSnq2k8LcGZvEtMt/izjFQICaJu
xvIbXBRsTqMmpNZiaDJU4i8NTbvfHBOSkx2Ip9dFQIVy9ijOuwg24VuXyCDY8Rzb
L/3Wkz/a1q4CJJSXgOpqQF60Dk8nYNRqEc2ykGkn/3GV/uqWbz0ohS1Wr55XiZeJ
fUSKmI72Yk6BVQAAAIEA0oaSAScm+gat8e6jAGpm1mHwf3iLI34NVgY3TzpL4kyz
Xk0OpxWMY5cgoXmWMnT1yCpun9SYBzyRhrfY8x7VPcNC9X96hNp/nIkp/FIWq/8M
TV2SIFcxidXpwMbGD8HXjAng+AkNYlK8ow/SDEkYsHWKuZsf99VqiHzgs5Y5U6kA
AACBAJ3N00Sgdv036FTLnU+NlF4N0kjhzjMDAPWRf9XvwkugiyB2tZ43rVCmXzgE
FzNeuOrWXPC7xh9Jfbg04rJv7sYZhSIIadTO3y3ToPXHpRNwg9pmC1BaQLMb0I5M
JUUNn5ASrFQki0/Ok5mwxz+QpktrvUuShkd/4e+sqHZ5mZ0n
Private-MAC: cceed3168be3c35863ebff8ff41457aa5ab449603b5660df1a4eea0201827c44`

const ppkEd25519Encrypted = `PuTTY-User-Key-File-2: ssh-ed25519
Encryption: aes256-cbc
Comment: a@b
Public-Lines: 2
AAAAC3NzaC1lZDI1NTE5AAAAIMb3N9pbqMpSJRFb/WF8Wcz80SiW8emW3aLFqdRA
rs+r
Private-Lines: 1
i6a/aAknwkK/cVT8nW9zcsOJDvOdPvfBlx0suOtygmSbz9L4yoBAZZu8AHxWDSgm
Private-MAC: 8fa9edfc1b94bec840ee1526d290bf1d8eb9fbc9`

const ppkEd25519Passphrase = "testkey"

func TestIsPPK(t *testing.T) {
	if !IsPPK([]byte(ppkRSAUnencrypted)) {
		t.Error("v3 ppk not detected")
	}
	if !IsPPK([]byte("  \n" + ppkEd25519Encrypted)) {
		t.Error("leading whitespace should not defeat detection")
	}
	if IsPPK([]byte("-----BEGIN OPENSSH PRIVATE KEY-----\n...")) {
		t.Error("OpenSSH key misdetected as ppk")
	}
	if IsPPK([]byte("just some text")) {
		t.Error("plain text misdetected as ppk")
	}
}

func TestConvertPPKUnencrypted(t *testing.T) {
	pem, err := ConvertPPKToOpenSSH([]byte(ppkRSAUnencrypted), "")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	// The result must be a valid OpenSSH key the rest of the app can parse.
	if _, err := ssh.ParsePrivateKey([]byte(pem)); err != nil {
		t.Fatalf("converted key does not parse as OpenSSH: %v", err)
	}
	if !strings.Contains(pem, "OPENSSH PRIVATE KEY") {
		t.Fatalf("expected an OpenSSH PEM, got: %.40q", pem)
	}
}

func TestConvertPPKEncrypted(t *testing.T) {
	pem, err := ConvertPPKToOpenSSH([]byte(ppkEd25519Encrypted), ppkEd25519Passphrase)
	if err != nil {
		t.Fatalf("convert (encrypted): %v", err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(pem))
	if err != nil {
		t.Fatalf("converted encrypted key does not parse: %v", err)
	}
	if got := signer.PublicKey().Type(); got != "ssh-ed25519" {
		t.Fatalf("algorithm: got %q, want ssh-ed25519", got)
	}
}

func TestConvertPPKMissingPassphrase(t *testing.T) {
	if _, err := ConvertPPKToOpenSSH([]byte(ppkEd25519Encrypted), ""); err == nil {
		t.Fatal("expected an error for an encrypted ppk with no passphrase")
	}
}

func TestConvertPPKWrongPassphrase(t *testing.T) {
	if _, err := ConvertPPKToOpenSSH([]byte(ppkEd25519Encrypted), "wrong"); err == nil {
		t.Fatal("expected an error for a wrong passphrase")
	}
}

func TestConvertPPKNotAPPK(t *testing.T) {
	if _, err := ConvertPPKToOpenSSH([]byte("-----BEGIN OPENSSH PRIVATE KEY-----\nx\n"), ""); err == nil {
		t.Fatal("expected an error for non-ppk input")
	}
}
