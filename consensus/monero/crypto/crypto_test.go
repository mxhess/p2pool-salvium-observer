package crypto

import (
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	fasthex "github.com/tmthrgd/go-hex"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func init() {
	_, filename, _, _ := runtime.Caller(0)
	// The ".." may change depending on you folder structure
	dir := path.Join(path.Dir(filename), "../..")
	err := os.Chdir(dir)
	if err != nil {
		panic(err)
	}

}

func GetTestEntries(name string, n int) chan []string {
	buf, err := os.ReadFile("testdata/v2_crypto_tests.txt")
	if err != nil {
		return nil
	}
	result := make(chan []string)
	go func() {
		defer close(result)
		for _, line := range strings.Split(string(buf), "\n") {
			entries := strings.Split(strings.TrimSpace(line), " ")
			if entries[0] == name && len(entries) >= (n+1) {
				result <- entries[1:]
			}
		}
	}()
	return result
}

func TestGenerateKeyDerivation(t *testing.T) {
	results := GetTestEntries("generate_key_derivation", 3)
	if results == nil {
		t.Fatal()
	}
	for e := range results {
		var expectedDerivation types.Hash

		key1 := PublicKeyBytes(types.MustHashFromString(e[0]))
		key2 := PrivateKeyBytes(types.MustHashFromString(e[1]))
		result := e[2] == "true"
		if result {
			expectedDerivation = types.MustHashFromString(e[3])
		}

		point := key1.AsPoint()
		scalar := key2.AsScalar()

		if result == false && (point == nil || scalar == nil) {
			//expected failure
			continue
		} else if point == nil || scalar == nil {
			t.Fatalf("invalid point %s / scalar %s", key1.String(), key2.String())
		}

		derivation := scalar.GetDerivationCofactor(point)
		if result {
			if expectedDerivation.String() != derivation.String() {
				t.Fatalf("expected %s, got %s", expectedDerivation.String(), derivation.String())
			}
		}
	}
}

func TestDeriveViewTag(t *testing.T) {
	results := GetTestEntries("derive_view_tag", 3)
	if results == nil {
		t.Fatal()
	}

	hasher := GetKeccak256Hasher()
	defer PutKeccak256Hasher(hasher)

	for e := range results {
		derivation := PublicKeyBytes(types.MustHashFromString(e[0]))
		outputIndex, _ := strconv.ParseUint(e[1], 10, 0)
		result, _ := fasthex.DecodeString(e[2])

		viewTag := GetDerivationViewTagForOutputIndex(&derivation, outputIndex)

		_, viewTag2 := GetDerivationSharedDataAndViewTagForOutputIndexNoAllocate(derivation, outputIndex, hasher)

		if viewTag != viewTag2 {
			t.Errorf("derive_view_tag differs from no_allocate: %d != %d", viewTag, &viewTag2)
		}

		if result[0] != viewTag {
			t.Errorf("expected %s, got %s", fasthex.EncodeToString(result), fasthex.EncodeToString([]byte{viewTag}))
		}
	}
}

func FuzzDeriveViewTag(f *testing.F) {
	results := GetTestEntries("derive_view_tag", 3)
	if results == nil {
		f.Fatal()
	}
	for e := range results {
		derivation := PublicKeyBytes(types.MustHashFromString(e[0]))
		outputIndex, _ := strconv.ParseUint(e[1], 10, 0)

		f.Add([]byte(derivation.AsSlice()), outputIndex)
	}

	f.Fuzz(func(t *testing.T, derivation []byte, outputIndex uint64) {
		hasher := GetKeccak256Hasher()
		defer PutKeccak256Hasher(hasher)

		derivationBytes := (*PublicKeySlice)(&derivation).AsBytes()

		viewTag := GetDerivationViewTagForOutputIndex(&derivationBytes, outputIndex)

		_, viewTag2 := GetDerivationSharedDataAndViewTagForOutputIndexNoAllocate(derivationBytes, outputIndex, hasher)

		if viewTag != viewTag2 {
			t.Fatalf("derive_view_tag differs from no_allocate: %d != %d", viewTag, &viewTag2)
		}
	})

}
