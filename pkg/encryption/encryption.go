package encryption

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"

	"github.com/flynn/noise"
	"golang.org/x/crypto/curve25519"
)

const (
	KeyLen          = 32
	handshakeMsgLen = KeyLen + 16
	maxMsgLen       = 140
)

// cipherSuite represents 25519_ChaChaPoly_BLAKE2s.
var cipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

type EncryptedPacketConn struct {
	remoteAddr net.Addr
	sendCipher *noise.CipherState
	recvCipher *noise.CipherState
	net.PacketConn
}

// newConfig instantiates configuration settings that are common to clients and
// servers.
func newConfig() noise.Config {
	return noise.Config{
		CipherSuite: cipherSuite,
		Pattern:     noise.HandshakeNK,
		Prologue:    []byte("dnstt 2020-04-13"),
	}
}

// ReadKey reads a hex-encoded key from r. r must consist of a single line, with
// or without a '\n' line terminator. The line must consist of KeyLen
// hex-encoded bytes.
func ReadKey(r io.Reader) ([]byte, error) {
	br := bufio.NewReader(io.LimitReader(r, 100))
	line, err := br.ReadString('\n')
	if err == io.EOF {
		err = nil
	}
	if err == nil {
		// Check that we're at EOF.
		_, err = br.ReadByte()
		if err == io.EOF {
			err = nil
		} else if err == nil {
			err = fmt.Errorf("file contains more than one line")
		}
	}
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(line, "\n")
	return DecodeKey(line)
}

// DecodeKey decodes a hex-encoded private or public key.
func DecodeKey(s string) ([]byte, error) {
	key, err := hex.DecodeString(s)
	if err == nil && len(key) != KeyLen {
		err = fmt.Errorf("length is %d, expected %d", len(key), KeyLen)
	}
	return key, err
}

// PubkeyFromPrivkey returns the public key that corresponds to privkey.
func PubkeyFromPrivkey(privkey []byte) []byte {
	pubkey, err := curve25519.X25519(privkey, curve25519.Basepoint)
	if err != nil {
		panic(err)
	}
	return pubkey
}

// Write writes encrypted data from the wrapped io.Writer.
func (e *EncryptedPacketConn) Write(p []byte) (int, error) {
	msg, err := e.sendCipher.Encrypt(nil, nil, p)
	if err != nil {
		return 0, err
	}
	length := uint8(len(msg))
	msgToSend := append([]byte{length}, msg...)
	return e.sendMsg(msgToSend)
}

func (e *EncryptedPacketConn) sendMsg(msg []byte) (int, error) {
	return e.WriteTo(msg, e.remoteAddr)
}

func (e *EncryptedPacketConn) recvMsg(msg []byte) (int, error) {
	var recvAddr net.Addr
	var readLen int
	var err error
	//for recvAddr != e.remoteAddr {
	readLen, recvAddr, err = e.ReadFrom(msg)
	//}
	_ = recvAddr
	return readLen, err
}

func (e *EncryptedPacketConn) Read(p []byte) (int, error) {
	var encryptedResponse [maxMsgLen]byte
	_, err := e.recvMsg(encryptedResponse[:])
	length := uint8(encryptedResponse[0])
	if err != nil {
		return 0, err
	}
	encryptedMsg := encryptedResponse[1 : 1+length]
	msg, err := e.recvCipher.Decrypt(nil, nil, encryptedMsg[:])
	if err != nil {
		return 0, nil
	}
	copy(p, msg)
	return len(msg), nil
}

func NewClient(pconn net.PacketConn, remote net.Addr, pubkey []byte) (*EncryptedPacketConn, error) {

	e := &EncryptedPacketConn{
		PacketConn: pconn,
		remoteAddr: remote,
	}
	config := newConfig()
	serverPubkey := pubkey
	config.Initiator = true
	config.PeerStatic = serverPubkey
	handshakeState, err := noise.NewHandshakeState(config)
	if err != nil {
		return nil, err
	}

	log.Println("start noise handshake")

	log.Println("-> e, es")
	// -> e, es
	msgToSend, _, _, err := handshakeState.WriteMessage(nil, nil)

	if err != nil {
		return nil, err
	}

	_, err = e.sendMsg(msgToSend)

	if err != nil {
		return nil, err
	}

	// <- e, es
	log.Println("<- e, es")
	var recvMsg [handshakeMsgLen]byte

	e.recvMsg(recvMsg[:])

	if err != nil {
		return nil, err
	}

	payload, sendCipher, recvCipher, err := handshakeState.ReadMessage(nil, recvMsg[:])

	if err != nil {
		return nil, err
	}
	if len(payload) != 0 {
		return nil, errors.New("unexpected server payload")
	}

	log.Println("noise handshake complete")

	e.sendCipher = sendCipher
	e.recvCipher = recvCipher
	return e, nil
}

func NewServer(pconn net.PacketConn, privkey []byte) (*EncryptedPacketConn, error) {

	e := &EncryptedPacketConn{
		PacketConn: pconn,
	}
	config := newConfig()
	config.Initiator = false
	config.StaticKeypair = noise.DHKey{
		Private: privkey,
		Public:  PubkeyFromPrivkey(privkey),
	}
	handshakeState, err := noise.NewHandshakeState(config)
	if err != nil {
		return nil, err
	}

	log.Println("start noise handshake")

	log.Println("-> e, es")
	// -> e, es

	var recvMsg [handshakeMsgLen]byte

	_, e.remoteAddr, err = e.ReadFrom(recvMsg[:])
	if err != nil {
		return nil, err
	}

	payload, _, _, err := handshakeState.ReadMessage(nil, recvMsg[:])

	if err != nil {
		return nil, err
	}

	if len(payload) != 0 {
		return nil, errors.New("unexpected server payload")
	}

	// <- e, es
	log.Println("<- e, es")

	msgToSend, recvCipher, sendCipher, err := handshakeState.WriteMessage(nil, nil)

	if err != nil {
		return nil, err
	}

	_, err = e.sendMsg(msgToSend)

	if err != nil {
		return nil, err
	}

	log.Println("noise handshake complete")

	e.sendCipher = sendCipher
	e.recvCipher = recvCipher
	return e, nil
}