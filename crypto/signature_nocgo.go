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

// +build nacl js nocgo

package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"errors"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/btcec"
)

// Ecrecover returns the uncompressed public key that created the given signature.
// Ecrecover는 주어진 서명으로부터 생성된 압축되지않은 공개키를 반환한다
func Ecrecover(hash, sig []byte) ([]byte, error) {
	pub, err := SigToPub(hash, sig)
	if err != nil {
		return nil, err
	}
	bytes := (*btcec.PublicKey)(pub).SerializeUncompressed()
	return bytes, err
}

// SigToPub returns the public key that created the given signature.
// SigToPub은 주어진 서명으로부터 공개키를 반환한다
func SigToPub(hash, sig []byte) (*ecdsa.PublicKey, error) {
	// Convert to btcec input format with 'recovery id' v at the beginning.
	// 시작부분에서 복원 id v를 이용하여 btcec 입력형태로 바꾼다
	btcsig := make([]byte, 65)
	btcsig[0] = sig[64] + 27
	copy(btcsig[1:], sig)

	pub, _, err := btcec.RecoverCompact(btcec.S256(), btcsig, hash)
	return (*ecdsa.PublicKey)(pub), err
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
func Sign(hash []byte, prv *ecdsa.PrivateKey) ([]byte, error) {
	if len(hash) != 32 {
		return nil, fmt.Errorf("hash is required to be exactly 32 bytes (%d)", len(hash))
	}
	if prv.Curve != btcec.S256() {
		return nil, fmt.Errorf("private key curve is not secp256k1")
	}
	sig, err := btcec.SignCompact(btcec.S256(), (*btcec.PrivateKey)(prv), hash, false)
	if err != nil {
		return nil, err
	}
	// Convert to Ethereum signature format with 'recovery id' v at the end.
	// 마지막에 recovery id v를 이용하여 이더리움 서명형태로 변환한다
	v := sig[0] - 27
	copy(sig, sig[1:])
	sig[64] = v
	return sig, nil
}

// VerifySignature checks that the given public key created signature over hash.
// The public key should be in compressed (33 bytes) or uncompressed (65 bytes) format.
// The signature should have the 64 byte [R || S] format.
// VerifySignature는 공개키가 해시를 통해 생성된 서명인지 검토한다
// 공개키는 반드시 33바이트로 압축되거나 65바이트의 비압축된 형태이다
// 서명은 반드시 64바이트의 [R || S] 형태여야 한다
func VerifySignature(pubkey, hash, signature []byte) bool {
	if len(signature) != 64 {
		return false
	}
	sig := &btcec.Signature{R: new(big.Int).SetBytes(signature[:32]), S: new(big.Int).SetBytes(signature[32:])}
	key, err := btcec.ParsePubKey(pubkey, btcec.S256())
	if err != nil {
		return false
	}
	// Reject malleable signatures. libsecp256k1 does this check but btcec doesn't.
	// 약한 서명을 거절함. libsecp256k1은 이체크를 하지만 , btcec는 하지 않는다
	if sig.S.Cmp(secp256k1halfN) > 0 {
		return false
	}
	return sig.Verify(hash, key)
}

// DecompressPubkey parses a public key in the 33-byte compressed format.
// DecompressPubkey는 33바이트 압축형태의 공개키를 파싱한다
func DecompressPubkey(pubkey []byte) (*ecdsa.PublicKey, error) {
	if len(pubkey) != 33 {
		return nil, errors.New("invalid compressed public key length")
	}
	key, err := btcec.ParsePubKey(pubkey, btcec.S256())
	if err != nil {
		return nil, err
	}
	return key.ToECDSA(), nil
}

// CompressPubkey encodes a public key to the 33-byte compressed format.
// CompressPubkey는 공개키를 33바이트의 압축포멧으로 인코딩한다
func CompressPubkey(pubkey *ecdsa.PublicKey) []byte {
	return (*btcec.PublicKey)(pubkey).SerializeCompressed()
}

// S256 returns an instance of the secp256k1 curve.
// S256은 secp256k1 커브의 인스턴스를 반환한다
func S256() elliptic.Curve {
	return btcec.S256()
}
