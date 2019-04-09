// Copyright 2017 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// +build !nacl,!js,!nocgo

package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"fmt"

	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
)

// Ecrecover returns the uncompressed public key that created the given signature.
// Ecrecover는 주어진 서명으로부터 생성된 압축되지않은 공개키를 반환한다
func Ecrecover(hash, sig []byte) ([]byte, error) {
	return secp256k1.RecoverPubkey(hash, sig)
}

// SigToPub returns the public key that created the given signature.
// SigToPub은 주어진 서명으로부터 공개키를 반환한다
func SigToPub(hash, sig []byte) (*ecdsa.PublicKey, error) {
	s, err := Ecrecover(hash, sig)
	if err != nil {
		return nil, err
	}

	x, y := elliptic.Unmarshal(S256(), s)
	return &ecdsa.PublicKey{Curve: S256(), X: x, Y: y}, nil
}

// Sign calculates an ECDSA signature.
//
// This function is susceptible to chosen plaintext attacks that can leak
// information about the private key that is used for signing. Callers must
// be aware that the given hash cannot be chosen by an adversery. Common
// solution is to hash any input before calculating the signature.
//
// The produced signature is in the [R || S || V] format where V is 0 or 1.
// Sing함수는 ECDSA서명을 계산한다
// 이함수는 서명에 사용된 개인키에 대한 정보를 누출할수 있는
// 선택된 순정값 공격에 민감하다
// 호출자는 반드시 주어진 해시가 상대방에 의해 선택될수 없도록 고려해야한다
// 일반적인 해결책은 서명을 계산하기전 아무 인풋에대한 해시를 하는것이다.
func Sign(hash []byte, prv *ecdsa.PrivateKey) (sig []byte, err error) {
	if len(hash) != 32 {
		return nil, fmt.Errorf("hash is required to be exactly 32 bytes (%d)", len(hash))
	}
	seckey := math.PaddedBigBytes(prv.D, prv.Params().BitSize/8)
	defer zeroBytes(seckey)
	return secp256k1.Sign(hash, seckey)
}

// VerifySignature checks that the given public key created signature over hash.
// The public key should be in compressed (33 bytes) or uncompressed (65 bytes) format.
// The signature should have the 64 byte [R || S] format.
// VerifySignature는 공개키가 해시를 통해 생성된 서명인지 검토한다
// 공개키는 반드시 33바이트로 압축되거나 65바이트의 비압축된 형태이다
// 서명은 반드시 64바이트의 [R || S] 형태여야 한다
func VerifySignature(pubkey, hash, signature []byte) bool {
	return secp256k1.VerifySignature(pubkey, hash, signature)
}

// DecompressPubkey parses a public key in the 33-byte compressed format.
// DecompressPubkey는 33바이트 압축형태의 공개키를 파싱한다
func DecompressPubkey(pubkey []byte) (*ecdsa.PublicKey, error) {
	x, y := secp256k1.DecompressPubkey(pubkey)
	if x == nil {
		return nil, fmt.Errorf("invalid public key")
	}
	return &ecdsa.PublicKey{X: x, Y: y, Curve: S256()}, nil
}

// CompressPubkey encodes a public key to the 33-byte compressed format.
// CompressPubkey는 공개키를 33바이트의 압축포멧으로 인코딩한다
func CompressPubkey(pubkey *ecdsa.PublicKey) []byte {
	return secp256k1.CompressPubkey(pubkey.X, pubkey.Y)
}

// S256 returns an instance of the secp256k1 curve.
// S256은 secp256k1 커브의 인스턴스를 반환한다
func S256() elliptic.Curve {
	return secp256k1.S256()
}
