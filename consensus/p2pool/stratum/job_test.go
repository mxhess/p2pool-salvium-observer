package stratum

import (
	"reflect"
	"testing"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
)

func TestJobRoundtrip(t *testing.T) {
	job := Job{
		TemplateCounter:  1,
		ExtraNonce:       2,
		SideRandomNumber: 3,
		SideExtraNonce:   4,
		MerkleRoot:       crypto.Keccak256Single([]byte{1}),
		MerkleProof:      []types.Hash{crypto.Keccak256Single([]byte{2}), crypto.Keccak256Single([]byte{3})},
	}
	job.MergeMiningExtra.Set(crypto.Keccak256Single([]byte{1}), []byte{1, 2, 3, 4})

	j2, err := JobFromString(job.Id())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(job, j2) {
		t.Fatalf("job %+v does not match job %+v", job, j2)
	}

	if j2.Id() != job.Id() {
		t.Fatalf("job %s does not match job %s", job.Id(), j2.Id())
	}
}
