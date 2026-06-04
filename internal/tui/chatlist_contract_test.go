package tui

// QUM-675 S5 contract test. Reflection-based tripwire: ChatList must NOT expose
// any of the four "contract violator" sinks. The omission has been structural
// since S1 (chatlist.go:11-13, items.go:14-16); this test pins it for future
// contributors so a well-meaning PR cannot reintroduce viewport-bleed via a
// ChatList method. See docs/designs/chatlist-invariants.md §3 + §10.
//
// This test PASSES today (chatlist.go has never had these methods). It is a
// forward-defending guard, not a red-phase target.

import (
	"reflect"
	"testing"
)

// forbiddenChatListMethods are the four "contract violator" sinks listed in
// docs/designs/chatlist-invariants.md §3. ChatList must expose ZERO of them.
var forbiddenChatListMethods = []string{
	"AppendStatus",
	"AppendError",
	"AppendBanner",
	"AppendSystemMessage",
}

// TestChatList_ContractViolators_AbsentFromPointerReceiver guards the pointer
// receiver method set. The dual-append shim invokes pointer-receiver methods
// on *ChatList, so this is the surface that matters most.
func TestChatList_ContractViolators_AbsentFromPointerReceiver(t *testing.T) {
	cl := NewChatList(nil)
	typ := reflect.TypeOf(cl) // *ChatList
	have := methodNameSet(typ)
	for _, forbidden := range forbiddenChatListMethods {
		if _, ok := have[forbidden]; ok {
			t.Errorf("ChatList (pointer receiver) must NOT expose %q — contract violator per docs/designs/chatlist-invariants.md §3; routing belongs on the status bar / γ overlay / tree badge", forbidden)
		}
	}
}

// TestChatList_ContractViolators_AbsentFromValueReceiver guards the value
// receiver method set too. Defends against a contributor adding a value-
// receiver convenience wrapper.
func TestChatList_ContractViolators_AbsentFromValueReceiver(t *testing.T) {
	var cl ChatList
	typ := reflect.TypeOf(cl) // ChatList (value)
	have := methodNameSet(typ)
	for _, forbidden := range forbiddenChatListMethods {
		if _, ok := have[forbidden]; ok {
			t.Errorf("ChatList (value receiver) must NOT expose %q — contract violator per docs/designs/chatlist-invariants.md §3", forbidden)
		}
	}
}

func methodNameSet(typ reflect.Type) map[string]struct{} {
	set := make(map[string]struct{}, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		set[typ.Method(i).Name] = struct{}{}
	}
	return set
}
