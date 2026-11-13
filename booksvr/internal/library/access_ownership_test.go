package library

import (
	"talebook/booksvr/internal/access"
	"testing"
)

func TestAccessPrivateDerivedOwnershipDoesNotBecomePublic(t *testing.T) {
	f := access.FileFacts{Ready: true, Parents: []access.FileFacts{{Ready: true, Private: true, PrivateOwner: "original"}}}
	owner := ""
	copy, ok := accessOwnershipFacts(f, &owner, 0)
	if !ok || owner != "original" || copy.Parents[0].Private || !f.Parents[0].Private {
		t.Fatal("ownership lost or input mutated", owner, copy, f)
	}
	f.Private = true
	f.PrivateOwner = "other"
	owner = ""
	if _, ok = accessOwnershipFacts(f, &owner, 0); ok {
		t.Fatal("conflicting owners accepted")
	}
}

func TestAccessMixedDerivedOwnershipIsOrderIndependent(t *testing.T) {
	public := AccessFileSnapshot{Facts: access.FileFacts{Ready: true}}
	private := AccessFileSnapshot{Facts: access.FileFacts{Ready: true, Parents: []access.FileFacts{{Ready: true, Private: true, PrivateOwner: "owner"}}}}
	for _, files := range [][]AccessFileSnapshot{{public, private}, {private, public}} {
		owner, ok := accessBookOwnership(files)
		if !ok || owner != "owner" {
			t.Fatal("private ancestry lost", owner, ok)
		}
	}
	other := AccessFileSnapshot{Facts: access.FileFacts{Private: true, PrivateOwner: "other"}}
	if _, ok := accessBookOwnership([]AccessFileSnapshot{private, other}); ok {
		t.Fatal("conflicting private owners accepted")
	}
}
