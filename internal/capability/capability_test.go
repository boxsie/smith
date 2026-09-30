package capability

import (
	"reflect"
	"testing"
)

func TestSortedIntentsNormalizesAndRejectsDuplicates(t *testing.T) {
	intents, err := SortedIntents([]string{" tickets_please.mutate ", "tickets_please.read"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tickets_please.mutate", "tickets_please.read"}
	if !reflect.DeepEqual(intents, want) {
		t.Fatalf("intents = %v, want %v", intents, want)
	}
	if _, err := SortedIntents([]string{"tickets_please.read", " tickets_please.read "}); err == nil {
		t.Fatal("normalized duplicate intent was accepted")
	}
}

func TestGrantAccessAndScopeAreSnapshotted(t *testing.T) {
	original := []Grant{{Package: "tickets_please", Access: AccessMutate, Scope: map[string]string{"project": "smith"}}}
	cloned := CloneGrants(original)
	original[0].Scope["project"] = "other"
	grant, err := MatchGrant(cloned, "tickets_please", AccessRead)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Access != AccessRead || grant.Scope["project"] != "smith" {
		t.Fatalf("matched grant = %#v", grant)
	}
	grant.Scope["project"] = "changed"
	if cloned[0].Scope["project"] != "smith" {
		t.Fatal("matched scope aliases stored authority")
	}
	if _, err := MatchGrant([]Grant{{Package: "tickets_please", Access: AccessRead}}, "tickets_please", AccessMutate); err == nil {
		t.Fatal("read grant satisfied mutate intent")
	}
}

func TestValidateGrantsRejectsAmbiguousPackages(t *testing.T) {
	for _, grants := range [][]Grant{
		{{Package: " tickets_please", Access: AccessRead}},
		{{Package: "tickets_please", Access: "write"}},
		{{Package: "tickets_please", Access: AccessRead}, {Package: "tickets_please", Access: AccessMutate}},
	} {
		if err := ValidateGrants(grants); err == nil {
			t.Fatalf("grants were accepted: %#v", grants)
		}
	}
}
