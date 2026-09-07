package pdf

import "testing"

func TestStackPushPopLen(t *testing.T) {
	var stk Stack
	if stk.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", stk.Len())
	}
	if v := stk.Pop(); v.Kind() != Null {
		t.Fatalf("Pop() on empty stack = %v, want Null", v.Kind())
	}

	stk.Push(testValue(int64(5)))
	stk.Push(testValue(int64(6)))
	if stk.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", stk.Len())
	}

	if v := stk.Pop(); v.Int64() != 6 {
		t.Fatalf("Pop() = %d, want 6 (LIFO)", v.Int64())
	}
	if v := stk.Pop(); v.Int64() != 5 {
		t.Fatalf("Pop() = %d, want 5", v.Int64())
	}
	if stk.Len() != 0 {
		t.Fatalf("Len() after pops = %d, want 0", stk.Len())
	}
}

func TestPopArgsOrder(t *testing.T) {
	var stk Stack
	stk.Push(testValue(int64(1)))
	stk.Push(testValue(int64(2)))
	stk.Push(testValue(int64(3)))

	args := popArgs(&stk)
	if len(args) != 3 {
		t.Fatalf("len(args) = %d, want 3", len(args))
	}
	for i, want := range []int64{1, 2, 3} {
		if args[i].Int64() != want {
			t.Fatalf("args[%d] = %d, want %d (bottom of stack first)", i, args[i].Int64(), want)
		}
	}
	if stk.Len() != 0 {
		t.Fatalf("stack not emptied by popArgs: Len() = %d", stk.Len())
	}
}
