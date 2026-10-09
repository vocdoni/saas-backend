package migrations

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestCollisionSkipsGroups(t *testing.T) {
	c := qt.New(t)

	member := func(id string) hashClaimant { return hashClaimant{participantID: id} }
	orphan := func(id string) hashClaimant { return hashClaimant{participantID: id, orphan: true} }

	occupied := map[string]map[string][]hashClaimant{
		"loginHash": {
			"aa": {member("b"), member("a")},         // a-b collide
			"bb": {member("c")},                      // c alone: no collision
			"cc": {member("e"), orphan("o1")},        // e collides with an orphan's stored hash
			"dd": {member("f"), member("g")},         // f-g ...
			"ee": {orphan("o2")},                     // lone orphan
			"ff": {member("x"), member("y")},         // x-y ...
			"11": {member("z"), member("zz")},        // z-zz
			"22": {member("d")},                      // d alone
			"33": {member("h"), member("z")},         // ... chained into z-zz
			"44": {member("a"), member("a-variant")}, // a also collides on loginHash with a-variant
		},
		"loginHashEmail": {
			"aa": {member("g"), member("x")}, // ... f-g and x-y joined through the email variant
		},
	}

	skipped, groups := collisionSkips(occupied)

	c.Assert(skipped, qt.HasLen, 11)
	for _, id := range []string{"a", "b", "a-variant", "e", "f", "g", "x", "y", "z", "zz", "h"} {
		_, ok := skipped[id]
		c.Assert(ok, qt.IsTrue, qt.Commentf("%s should be skipped", id))
	}
	for _, id := range []string{"c", "d", "o1", "o2"} {
		_, ok := skipped[id]
		c.Assert(ok, qt.IsFalse, qt.Commentf("%s should not be skipped", id))
	}

	c.Assert(groups, qt.DeepEquals, []CollisionGroup{
		{MemberIDs: []string{"a", "a-variant", "b"}},
		{MemberIDs: []string{"e"}, OrphanMemberIDs: []string{"o1"}},
		{MemberIDs: []string{"f", "g", "x", "y"}},
		{MemberIDs: []string{"h", "z", "zz"}},
	})
}

func TestCollisionSkipsNoCollisions(t *testing.T) {
	c := qt.New(t)
	skipped, groups := collisionSkips(map[string]map[string][]hashClaimant{
		"loginHash": {
			"aa": {{participantID: "a"}},
			"bb": {{participantID: "b", orphan: true}},
		},
	})
	c.Assert(skipped, qt.HasLen, 0)
	c.Assert(groups, qt.HasLen, 0)
}

func TestStaleFields(t *testing.T) {
	c := qt.New(t)
	current := participantHashSet{LoginHash: []byte{1}, LoginHashEmail: []byte{2}}

	c.Assert(current.staleFields(participantHashRow{LoginHash: []byte{1}, LoginHashEmail: []byte{2}}), qt.HasLen, 0)
	c.Assert(current.staleFields(participantHashRow{}), qt.HasLen, 0)
	c.Assert(current.staleFields(participantHashRow{
		LoginHash: []byte{9}, LoginHashEmail: []byte{2}, LoginHashPhone: []byte{3},
	}), qt.DeepEquals, []string{"loginHash", "loginHashPhone"})
}
