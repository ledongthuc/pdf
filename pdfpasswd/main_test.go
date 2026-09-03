package main

import (
	"reflect"
	"testing"
)

func TestInc(t *testing.T) {
	cases := []struct {
		in   []int
		base int
		want []int
	}{
		{[]int{0, 0}, 3, []int{1, 0}},
		{[]int{1, 0}, 3, []int{2, 0}},
		{[]int{2, 0}, 3, []int{0, 1}}, // carry
		{[]int{2, 2}, 3, []int{0, 0}}, // carry all the way
	}
	for _, tc := range cases {
		in := append([]int(nil), tc.in...)
		inc(in, tc.base)
		if !reflect.DeepEqual(in, tc.want) {
			t.Errorf("inc(%v, %d) = %v, want %v", tc.in, tc.base, in, tc.want)
		}
	}
}

func TestDone(t *testing.T) {
	if !done([]int{0, 0, 0}) {
		t.Fatal("done(all zero) = false, want true")
	}
	if done([]int{1, 0, 0}) {
		t.Fatal("done(non-zero) = true, want false")
	}
}

func TestValid(t *testing.T) {
	cases := []struct {
		in   []int
		want bool
	}{
		{[]int{0, 0}, true}, // all zero == the terminating state
		{[]int{1, 0}, true}, // no leading zeros
		{[]int{1, 2}, true},
		{[]int{0, 1}, false}, // leading zero before a non-zero digit
		{[]int{1, 0, 2}, false},
	}
	for _, tc := range cases {
		if got := valid(tc.in); got != tc.want {
			t.Errorf("valid(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
