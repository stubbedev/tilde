package xgb

import "testing"

func TestRegisteringAnExtensionDoesNotRaceItsReaders(t *testing.T) {
	const name, first = "XGB-REGISTER-TEST", 200
	NewExtEventFuncs[name] = map[int]NewEventFun{0: func([]byte) Event { return nil }}
	NewExtErrorFuncs[name] = map[int]NewErrorFun{0: func([]byte) Error { return nil }}
	c := &Conn{Extensions: map[string]byte{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			newEventFunc(first)
			newErrorFunc(first)
		}
	}()
	for range 1000 {
		RegisterExtension(c, name, 1, first, first)
	}
	<-done
	if _, ok := newEventFunc(first); !ok {
		t.Fatal("the extension's event constructor is not registered")
	}
	if _, ok := newErrorFunc(first); !ok {
		t.Fatal("the extension's error constructor is not registered")
	}
	if c.Extensions[name] != 1 {
		t.Fatalf("the extension's major opcode is %d, want 1", c.Extensions[name])
	}
}
