package qoder

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// COSY is Qoder's request signature, ported from qodercli: the caller's identity goes out AES-128-CBC encrypted with
// a fresh key, the key itself RSA-encrypted, and an MD5 over payload + key + timestamp + body + path rides in
// Authorization. The upstream recomputes that MD5 from the very bytes it received, so the body must be signed exactly
// as sent (i.e. after encodeBody).
const rsaPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

// The values the upstream compares its own client against; not arbitrary.
const (
	ideVersion  = "1.0.0"
	clientType  = "5"
	dataPolicy  = "disagree"
	loginVer    = "v2"
	machineOS   = "x86_64_windows"
	machineType = "5"
)

var rsaKey = func() *rsa.PublicKey {
	b, _ := pem.Decode([]byte(rsaPublicKey))
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		panic("qoder: embedded RSA key does not parse: " + err.Error())
	}
	return k.(*rsa.PublicKey)
}()

type cosyCreds struct {
	UserID    string
	Token     string
	MachineID string
}

// machineID derives a stable machine uuid from the user id: the server keys its model queue per client, so a random
// id per request would put every call behind a brand-new device.
func machineID(seed string) string {
	if seed == "" {
		return uuid.NewString()
	}
	h := sha256.Sum256([]byte("qoder-machine\x00" + seed))
	s := hex.EncodeToString(h[:])
	return s[0:8] + "-" + s[8:12] + "-5" + s[13:16] + "-a" + s[17:20] + "-" + s[20:32]
}

// cosyHeaders signs body (the bytes about to be sent) for requestURL.
func cosyHeaders(body []byte, requestURL string, c cosyCreds) (map[string]string, error) {
	if c.UserID == "" {
		return nil, errors.New("qoder credential is missing userId; use a Personal Access Token so it can be resolved")
	}
	if c.Token == "" {
		return nil, errors.New("qoder credential is missing a token")
	}
	aesKey := uuid.NewString()[:16]
	info, err := json.Marshal(map[string]any{"uid": c.UserID, "security_oauth_token": c.Token, "name": "", "aid": "", "email": ""})
	if err != nil {
		return nil, err
	}
	sealed, err := sealInfo(info, aesKey)
	if err != nil {
		return nil, err
	}

	wrapped, err := rsa.EncryptPKCS1v15(rand.Reader, rsaKey, []byte(aesKey))
	if err != nil {
		return nil, err
	}
	cosyKey := base64.StdEncoding.EncodeToString(wrapped)

	requestID := uuid.NewString()
	payload, err := json.Marshal(map[string]any{
		"version": "v1", "requestId": requestID, "info": base64.StdEncoding.EncodeToString(sealed),
		"cosyVersion": ideVersion, "ideVersion": "",
	})
	if err != nil {
		return nil, err
	}
	payload64 := base64.StdEncoding.EncodeToString(payload)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	sigPath := sigPathOf(requestURL)

	sum := md5.Sum([]byte(payload64 + "\n" + cosyKey + "\n" + stamp + "\n" + string(body) + "\n" + sigPath))
	bodySum := md5.Sum(body)
	machine := c.MachineID
	if machine == "" {
		machine = machineID(c.UserID)
	}
	return map[string]string{
		"Authorization":          "Bearer COSY." + payload64 + "." + hex.EncodeToString(sum[:]),
		"Cosy-Key":               cosyKey,
		"Cosy-User":              c.UserID,
		"Cosy-Date":              stamp,
		"Cosy-Version":           ideVersion,
		"Cosy-Machineid":         machine,
		"Cosy-Machinetoken":      machine,
		"Cosy-Machinetype":       machineType,
		"Cosy-Machineos":         machineOS,
		"Cosy-Clienttype":        clientType,
		"Cosy-Clientip":          "127.0.0.1",
		"Cosy-Bodyhash":          hex.EncodeToString(bodySum[:]),
		"Cosy-Bodylength":        strconv.Itoa(len(body)),
		"Cosy-Sigpath":           sigPath,
		"Cosy-Data-Policy":       dataPolicy,
		"Cosy-Organization-Id":   "",
		"Cosy-Organization-Tags": "",
		"Login-Version":          loginVer,
		"X-Request-Id":           uuid.NewString(),
	}, nil
}

// sealInfo is AES-128-CBC over the padded identity JSON, with the key bytes reused as the IV (qodercli does the same).
func sealInfo(plain []byte, aesKey string) ([]byte, error) {
	block, err := aes.NewCipher([]byte(aesKey))
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytesRepeat(byte(pad), pad)...)
	sealed := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(aesKey)).CryptBlocks(sealed, padded)
	return sealed, nil
}

// sigPathOf is the request path without its leading /algo, which is what the signature covers.
func sigPathOf(requestURL string) string {
	u, err := url.Parse(requestURL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/algo")
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
